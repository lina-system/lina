// SPDX-License-Identifier: Apache-2.0

import Foundation
import FoundationModels

struct HelperRequest: Decodable {
    let id: String
    let op: String
    let model: String?
    let instructions: String?
    let input: String?
    let schema: SchemaSpec?
}

struct SchemaSpec: Decodable {
    let type: String
    let properties: [String: FieldSpec]
    let required: [String]
    let additionalProperties: Bool
}

struct FieldSpec: Decodable {
    let type: String
}

@main
struct AppleFMHelper {
    static func main() async {
        let request: HelperRequest
        do {
            guard let line = readLine() else { throw HelperFailure() }
            request = try JSONDecoder().decode(HelperRequest.self, from: Data(line.utf8))
        } catch {
            fputs("invalid helper request\n", stderr)
            exit(1)
        }
        if request.model == "apple-foundation-private-cloud" {
            reply(["id": request.id, "ok": false, "unavailable": true])
            return
        }
        let model = SystemLanguageModel.default
        guard case .available = model.availability else {
            reply(["id": request.id, "ok": false, "unavailable": true])
            return
        }
        if request.op == "profile" {
            reply([
                "id": request.id,
                "ok": true,
                "profile": [
                    "context_tokens": model.maxContextSize,
                    "tool_calling": true,
                    "guided_generation": true,
                    "image_input": false,
                    "model": "apple-foundation-on-device",
                    "private_cloud": false,
                ],
            ])
            return
        }
        guard request.op == "complete", let input = request.input, !input.isEmpty else {
            reply(["id": request.id, "ok": false, "unavailable": true])
            return
        }
        let session = LanguageModelSession(model: model, instructions: request.instructions ?? "Reply briefly.")
        do {
            if let schema = request.schema {
                let generation = try generationSchema(schema)
                let response = try await session.respond(to: input, schema: generation)
                let object = try jsonObject(response.content, schema: schema)
                let data = try JSONSerialization.data(withJSONObject: object)
                let encoded = try JSONSerialization.jsonObject(with: data)
                reply(["id": request.id, "ok": true, "schema_ok": true, "object": encoded])
            } else {
                let response = try await session.respond(to: input)
                reply(["id": request.id, "ok": true, "text": response.content])
            }
        } catch {
            reply(["id": request.id, "ok": false, "schema_ok": false])
        }
    }
}

private func generationSchema(_ schema: SchemaSpec) throws -> GenerationSchema {
    guard schema.type == "object", schema.additionalProperties == false, schema.required.count == schema.properties.count else {
        throw HelperFailure()
    }
    let properties = try schema.required.map { name -> DynamicGenerationSchema.Property in
        guard let field = schema.properties[name] else { throw HelperFailure() }
        return DynamicGenerationSchema.Property(name: name, schema: try fieldSchema(field.type))
    }
    let root = DynamicGenerationSchema(name: "Result", properties: properties)
    return try GenerationSchema(root: root, dependencies: [])
}

private func fieldSchema(_ type: String) throws -> DynamicGenerationSchema {
    switch type {
    case "string":
        return DynamicGenerationSchema(type: String.self)
    case "integer":
        return DynamicGenerationSchema(type: Int.self)
    case "boolean":
        return DynamicGenerationSchema(type: Bool.self)
    default:
        throw HelperFailure()
    }
}

private func jsonObject(_ content: GeneratedContent, schema: SchemaSpec) throws -> [String: Any] {
    var object: [String: Any] = [:]
    for name in schema.required {
        guard let field = schema.properties[name] else { throw HelperFailure() }
        switch field.type {
        case "string":
            object[name] = try content.value(String.self, forProperty: name)
        case "integer":
            object[name] = try content.value(Int.self, forProperty: name)
        case "boolean":
            object[name] = try content.value(Bool.self, forProperty: name)
        default:
            throw HelperFailure()
        }
    }
    return object
}

private struct HelperFailure: Error {}

private func reply(_ body: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: body),
          let text = String(data: data, encoding: .utf8) else {
        fputs("invalid helper response\n", stderr)
        exit(1)
    }
    print(text)
    exit(0)
}
