// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "LINAHelper",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "HelperCore", targets: ["HelperCore"]),
        .executable(name: "lina-helper", targets: ["HelperApp"]),
        .executable(name: "lina-helperctl", targets: ["HelperCLI"]),
    ],
    targets: [
        .target(name: "HelperCore"),
        .executableTarget(name: "HelperApp", dependencies: ["HelperCore"]),
        .executableTarget(name: "HelperCLI", dependencies: ["HelperCore"]),
        .testTarget(name: "HelperCoreTests", dependencies: ["HelperCore"]),
    ]
)
