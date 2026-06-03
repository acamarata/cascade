import SwiftUI

struct ContentView: View {
    @State private var cache: CascadeCache? = CascadeCache.load()
    @State private var lastRefresh = Date()

    var body: some View {
        VStack(spacing: 20) {
            HStack(spacing: 8) {
                Circle()
                    .fill(cache?.isStale == false ? Color.green : Color.orange)
                    .frame(width: 8, height: 8)
                Text("Cascade")
                    .font(.headline)
            }

            if let cache = cache {
                Text("Cache: " + cache.ageString)
                    .foregroundColor(.secondary)
                Text("\(cache._widget?.projects?.count ?? 0) projects · \(cache._widget?.totals?.active ?? 0) active tasks")
            } else {
                Text("No cache found. Run `cascade` first.")
                    .foregroundColor(.orange)
            }

            Button("Open Dashboard") {
                // Port 9761 is the cascade dashboard default (see install.sh DASHBOARD_PORT)
                if let url = URL(string: "http://localhost:9761") {
                    NSWorkspace.shared.open(url)
                }
            }
            .buttonStyle(.borderedProminent)

            Text("Widget reads from: ~/Library/Group Containers/group.io.cascade/cache.json")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(24)
        .frame(width: 360, height: 240)
        .onAppear { cache = CascadeCache.load() }
    }
}
