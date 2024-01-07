package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/lmittmann/tint"
)

type Config struct {
	specFile    string
	contentType string
	port        int
	logLevel    slog.Level
}

var config = Config{
	specFile:    "",
	contentType: "application/json",
	port:        8000,
	logLevel:    slog.LevelInfo,
}
var logger *slog.Logger

func main() {
	// Command line flags
	var levelString string
	flag.StringVar(&config.specFile, "file", "", "OpenAPI spec file, can be JSON or YAML")
	flag.StringVar(&config.specFile, "f", "", "OpenAPI spec file, can be JSON or YAML")
	flag.StringVar(&config.contentType, "content-type", "application/json", "Default content type to use in responses")
	flag.IntVar(&config.port, "port", 8000, "Port to run mock server on")
	flag.StringVar(&levelString, "log-level", "info", "Log level: debug, info, warn, error (default info))")
	flag.Parse()

	if config.specFile == "" {
		logger.Error("No OpenAPI spec file specified, please use -file or -f")
		os.Exit(1)
	}

	w := os.Stderr
	levelString = strings.ToLower(levelString)
	if levelString == "debug" {
		config.logLevel = slog.LevelDebug
	} else if levelString == "info" {
		config.logLevel = slog.LevelInfo
	} else if levelString == "warn" {
		config.logLevel = slog.LevelWarn
	} else if levelString == "error" {
		config.logLevel = slog.LevelError
	}

	logger = slog.New(tint.NewHandler(w, &tint.Options{
		Level: config.logLevel,
	}))

	// Load spec file
	spec, err := ParseV2Spec(config.specFile)
	if err != nil {
		logger.Error("Failed to parse OpenAPI spec file:", tint.Err(err))
		os.Exit(1)
	}

	r := chi.NewRouter()
	//r.Use(middleware.Logger)

	// Handle base path
	basePath := spec.BasePath

	// If base path doesn't start with a slash it's malformed
	if basePath == "" || basePath[:1] != "/" {
		logger.Warn("Base path maybe invalid, it will be ignored", slog.Any("basePath", basePath))
		basePath = "/"
	}

	if basePath[len(basePath)-1:] == "/" {
		basePath = basePath[:len(basePath)-1]
	}

	// Nice stuff
	if spec.Info.Title != "" {
		ver := spec.Info.Version
		logger.Warn("Starting API Mocker", slog.Any("title", spec.Info.Title), slog.Any("version", ver))
	}

	// Ignore CORS
	cors := cors.AllowAll()
	r.Use(cors.Handler)

	// Add server headers
	r.Use(middleware.SetHeader("Server", fmt.Sprintf("API Mocker: %s v%s", spec.Info.Title, spec.Info.Version)))

	// Loop over all paths
	for path, pathSpec := range spec.Paths {
		if path[:1] != "/" {
			continue
		}

		fullPath := basePath + path
		if pathSpec.isGet() {
			logger.Info("🔵 Adding GET route", slog.Any("path", fullPath))
			r.Get(fullPath, createResponseHandler(pathSpec.Get, spec.Definitions))
		}

		if pathSpec.isPost() {
			logger.Info("🟢 Adding POST route", slog.Any("path", fullPath))
			r.Post(fullPath, createResponseHandler(pathSpec.Post, spec.Definitions))
		}

		if pathSpec.isPut() {
			logger.Info("🟠 Adding PUT route", slog.Any("path", fullPath))
			r.Put(fullPath, createResponseHandler(pathSpec.Put, spec.Definitions))
		}

		if pathSpec.isDelete() {
			logger.Info("🔴 Adding DELETE route", slog.Any("path", fullPath))
			r.Delete(fullPath, createResponseHandler(pathSpec.Delete, spec.Definitions))
		}
	}

	logger.Warn("Server started", slog.Any("port", config.port))
	_ = http.ListenAndServe(fmt.Sprintf(":%d", config.port), r)
}

func createResponseHandler(op Operation, defs map[string]Schema) http.HandlerFunc {
	logger.Info("   Creating handler", slog.Any("id", op.OperationId), slog.Any("title", op.Description))

	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("Handling request", slog.Any("path", r.URL.Path), slog.Any("method", r.Method), slog.Any("id", op.OperationId))

		// Get x-mock-response-code header which allows caller to request a specific response
		requestedCode := r.Header.Get("x-mock-response-code")

		// Path to discover which response to use
		expectedStatus := 200
		if requestedCode != "" {
			expectedStatus, _ = strconv.Atoi(requestedCode)
		}

		respIndex := strconv.Itoa(expectedStatus)
		statusCode := expectedStatus

		resp, respExists := op.Responses[respIndex]
		if !respExists && requestedCode != "" {
			// No 200 response, fall back to first one in map
			for key := range op.Responses {
				respIndex = key
				statusCode, _ = strconv.Atoi(key)
				resp = op.Responses[key]
				break
			}
		}

		payload := parseResponseExample(resp, respIndex, defs)

		if payload != nil {
			logger.Info("Returnng example payload")
			w.Header().Set("Content-Type", config.contentType)
			w.WriteHeader(statusCode)
			_ = json.NewEncoder(w).Encode(payload)
		} else {
			logger.Warn("No example found", slog.Any("status", respIndex))
			w.WriteHeader(statusCode)
		}
	}
}

func parseResponseExample(resp Response, respName string, defs map[string]Schema) interface{} {
	logger.Info("Examining response for example", slog.Any("status", respName), slog.Any("description", resp.Description))
	// Simple case 1: Response has examples defined per content type
	if resp.Examples != nil {
		ex := resp.Examples[config.contentType]
		if ex != nil {
			return ex
		}
	}

	// Simple case 2: Schema has an example object
	if resp.Schema.Example != nil {
		return resp.Schema.Example
	}

	// Complex case: Schema has more complex structure
	if resp.Schema.Type != "" || resp.Schema.Ref != "" || resp.Schema.Items.Type != "" || resp.Schema.Items.Ref != "" {
		return parseSchema(resp.Schema, defs)
	}

	return nil
}

func parseSchema(schema Schema, models map[string]Schema) interface{} {
	logger.Debug("Parsing schema", slog.Any("schema", schema))

	var ref string
	if schema.Ref != "" {
		ref = schema.Ref
	}
	if schema.Items.Ref != "" {
		ref = schema.Items.Ref
	}

	// Resolve references
	if ref != "" {
		// Split ref into parts
		refParts := strings.Split(ref, "/")
		if len(refParts) != 3 {
			return nil
		}

		// Get model definition
		referencedSchema, defExists := models[refParts[2]]
		if !defExists {
			return nil
		}

		// Parse definition
		logger.Info("Parsing definition", slog.Any("name", refParts[2]))
		parsedSchema := parseSchema(referencedSchema, models)

		// If it's an array, return an array of the parsed schema
		if schema.Type == "array" {
			return []interface{}{parsedSchema}
		}

		return parsedSchema
	}

	if schema.Items.Type != "" {
		return parseItems(schema.Items)
	}

	if schema.Properties != nil {
		return parseProperties(schema.Properties)
	}

	return nil
}

func parseItems(items Items) interface{} {
	if items.Properties != nil {
		return parseProperties(items.Properties)
	}

	return nil
}

func parseProperties(properties map[string]Properties) interface{} {
	payload := make(map[string]interface{})
	for key, prop := range properties {
		var exampleVal any
		if prop.Example == nil {
			switch prop.Type {
			case "string":
				exampleVal = "string"
			case "integer":
				exampleVal = 0
			case "boolean":
				exampleVal = false
			case "array":
				exampleVal = []string{}
			case "object":
				exampleVal = make(map[string]interface{})
			}
		} else {
			exampleVal = prop.Example
		}

		payload[key] = exampleVal
	}

	return payload
}