import Foundation
import Darwin

public struct CLIOptions: Sendable {
    public let request: Request
    public let output: String?
    public let policyOnly: Bool

    public func responseLine(_ response: Response) throws -> Data {
        guard policyOnly, response.ok else { return try response.line() }
        guard let policy = response.result?["policy"]?.object else {
            throw HelperError(.internalError, "status did not return a policy")
        }
        return try JSONEncoder().encode(["policy": JSONValue.object(policy)]) + Data([10])
    }
    public static func parse(_ arguments: [String], environment: [String: String] = ProcessInfo.processInfo.environment) throws -> Self {
        guard let action = arguments.first, !action.isEmpty, !action.hasPrefix("-") else {
            throw HelperError(.badRequest, "usage: lina-helperctl ACTION [--arg k=v]... [--json ARGS] [--out PATH|-] [--caller NAME]")
        }
        var args: [String: JSONValue] = [:]
        var caller = "pid:\(getppid())"
        var output: String?
        var index = 1
        while index < arguments.count {
            let option = arguments[index]
            guard index + 1 < arguments.count else { throw HelperError(.badRequest, "option requires a value") }
            let value = arguments[index + 1]
            switch option {
            case "--arg":
                guard let split = value.firstIndex(of: "="), split != value.startIndex else {
                    throw HelperError(.badRequest, "--arg expects k=v")
                }
                args[String(value[..<split])] = .string(String(value[value.index(after: split)...]))
            case "--json":
                guard let object = try? JSONDecoder().decode([String: JSONValue].self, from: Data(value.utf8)) else {
                    throw HelperError(.badRequest, "--json expects an object")
                }
                args.merge(object) { _, new in new }
            case "--caller": caller = value
            case "--out":
                guard !value.isEmpty else { throw HelperError(.badRequest, "--out expects a path or -") }
                output = value
            default: throw HelperError(.badRequest, "unknown option")
            }
            index += 2
        }
        if action == "policy", output != nil {
            throw HelperError(.badRequest, "policy does not return a file")
        }
        let request = Request(action: action == "policy" ? "status" : action, args: args, caller: caller, sshClient: environment["SSH_CLIENT"], parentPID: getppid())
        _ = try Request.decode(JSONEncoder().encode(request))
        return Self(request: request, output: output, policyOnly: action == "policy")
    }
}

public enum CLI {
    public static func run(arguments: [String], environment: [String: String] = ProcessInfo.processInfo.environment) -> Int32 {
        var id = ""
        do {
            let options = try CLIOptions.parse(arguments, environment: environment)
            id = options.request.id
            let response = try Transport.exchange(options.request, path: Configuration(environment: environment).socketPath)
            let json = try options.responseLine(response)
            // A byte stream uses stdout; its accompanying JSON goes to stderr.
            let jsonHandle = options.output == "-" ? FileHandle.standardError : FileHandle.standardOutput
            if response.ok, let output = options.output {
                guard let path = response.result?["path"]?.string else {
                    throw HelperError(.badRequest, "action did not return a file")
                }
                let source = try FileHandle(forReadingFrom: URL(fileURLWithPath: path))
                defer { try? source.close() }
                if output == "-" {
                    while let data = try source.read(upToCount: 64 * 1024), !data.isEmpty {
                        try FileHandle.standardOutput.write(contentsOf: data)
                    }
                } else {
                    // copyItem refuses overwrites; partial copies are removed by Foundation.
                    try FileManager.default.copyItem(atPath: path, toPath: output)
                }
            }
            try jsonHandle.write(contentsOf: json)
            return response.ok ? 0 : 1
        } catch {
            let failure = (error as? HelperError) ?? HelperError(.internalError, "CLI I/O failed")
            if let data = try? Response.failure(id: id, failure).line() {
                try? FileHandle.standardError.write(contentsOf: data)
            }
            return 1
        }
    }
}
