// swift-tools-version: 5.9
// Package.swift — Cascade macOS WidgetKit extension
// Purpose: Declares the Swift package for the Cascade status widget
// Inputs: Xcode 15+, macOS 13+, WidgetKit, AppIntents, SwiftUI
// Outputs: CascadeWidget.appex bundle
// SPORT: apps/cascade-widget-macos

import PackageDescription

let package = Package(
    name: "CascadeWidget",
    platforms: [
        .macOS(.v13)
    ],
    products: [
        .library(
            name: "CascadeWidget",
            targets: ["CascadeWidget"]
        )
    ],
    dependencies: [],
    targets: [
        .target(
            name: "CascadeWidget",
            dependencies: [],
            path: "Sources/CascadeWidget",
            swiftSettings: [
                .enableExperimentalFeature("StrictConcurrency")
            ]
        ),
        .testTarget(
            name: "CascadeWidgetTests",
            dependencies: ["CascadeWidget"],
            path: "Tests/CascadeWidgetTests"
        )
    ]
)
