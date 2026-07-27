#!/usr/bin/env ruby

require "yaml"

class OpenAPIError < StandardError; end

def load_document(path, documents)
  path = File.expand_path(path)
  documents[path] ||= YAML.safe_load(File.read(path))
rescue Errno::ENOENT, Psych::Exception => error
  raise OpenAPIError, "cannot load #{path}: #{error.message}"
end

def resolve_reference(reference, path, documents)
  file, pointer = reference.split("#", 2)
  document_path = file.empty? ? path : File.expand_path(file, File.dirname(path))
  value = load_document(document_path, documents)
  return [document_path, "", value] if pointer.nil? || pointer.empty?
  raise OpenAPIError, "invalid local reference #{reference.inspect}" unless pointer.start_with?("/")

  pointer.split("/")[1..].each do |part|
    part = part.gsub("~1", "/").gsub("~0", "~")
    value = case value
            when Hash then value.fetch(part) { raise OpenAPIError, "unresolved local reference #{reference.inspect}" }
            when Array then value.fetch(Integer(part, 10)) { raise OpenAPIError, "unresolved local reference #{reference.inspect}" }
            else raise OpenAPIError, "unresolved local reference #{reference.inspect}"
            end
  end
  [document_path, pointer, value]
rescue ArgumentError, IndexError
  raise OpenAPIError, "unresolved local reference #{reference.inspect}"
end

def local_reference?(reference)
  reference.start_with?("#") || reference !~ %r{^(?:[a-z][a-z0-9+.-]*:|//)}i
end

def resolve_local_references(value, path, documents, seen = {})
  case value
  when Hash
    reference = value["$ref"]
    if reference
      raise OpenAPIError, "$ref must be a string" unless reference.is_a?(String)
      if local_reference?(reference)
        target_path, pointer, target = resolve_reference(reference, path, documents)
        key = [target_path, pointer]
        unless seen[key]
          seen[key] = true
          resolve_local_references(target, target_path, documents, seen)
        end
      end
    end
    value.each_value { |child| resolve_local_references(child, path, documents, seen) }
  when Array
    value.each { |child| resolve_local_references(child, path, documents, seen) }
  end
end

def require_value(value, expected, name)
  raise OpenAPIError, "#{name}=#{value.inspect}, want #{expected.inspect}" unless value == expected
end

document_path = File.expand_path("../docs/openapi.yaml", __dir__)
documents = {}
document = load_document(document_path, documents)
resolve_local_references(document, document_path, documents)

upload = document.dig("paths", "/v1/scip/uploads", "post", "requestBody", "content", "application/vnd.scip+protobuf", "schema")
graph_upload = document.dig("paths", "/v1/graph/uploads", "post", "requestBody", "content", "application/vnd.grepnest.graph.v1+protobuf", "schema")
graph_status = document.dig("paths", "/v1/graph/repositories/{id}/status", "get", "responses", "200", "content", "application/json", "schema")
locations = document.dig("components", "schemas", "SCIPNavigationResponse", "properties", "locations")
raise OpenAPIError, "SCIP upload schema is missing" unless upload.is_a?(Hash)
raise OpenAPIError, "graph upload schema is missing" unless graph_upload.is_a?(Hash)
raise OpenAPIError, "graph status schema is missing" unless graph_status.is_a?(Hash)
raise OpenAPIError, "SCIP navigation locations schema is missing" unless locations.is_a?(Hash)

require_value(upload["x-default-max-bytes"], 67_108_864, "SCIP upload default byte cap")
require_value(upload["x-server-max-bytes"], 268_435_456, "SCIP upload server byte cap")
require_value(graph_upload["x-default-max-bytes"], 67_108_864, "graph upload default byte cap")
require_value(graph_upload["x-server-max-bytes"], 268_435_456, "graph upload server byte cap")
require_value(graph_status["$ref"], "#/components/schemas/GraphStatus", "graph status response schema")
require_value(locations["maxItems"], 100, "SCIP navigation locations cap")

puts "OpenAPI validation passed"
