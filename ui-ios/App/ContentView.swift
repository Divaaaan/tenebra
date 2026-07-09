// SCAFFOLD — NOT compiled or verified (no Xcode/Swift on the authoring host).
// A minimal SwiftUI surface: a connect toggle plus a node list, bound to the
// TunnelManager. The real client grows a settings screen, subscription import UI,
// per-node latency, traffic stats, etc. — this is just enough to show the wiring.
// Build and refine on macOS + Xcode (see ui-ios/README.md).

import SwiftUI
import NetworkExtension

struct ContentView: View {
    @EnvironmentObject private var tunnel: TunnelManager

    var body: some View {
        NavigationStack {
            List {
                Section("Connection") {
                    Toggle("VPN", isOn: connectBinding)
                    LabeledContent("Status", value: tunnel.statusText)
                    if let node = tunnel.selectedNode {
                        LabeledContent("Node", value: node.name)
                    }
                }

                Section("Nodes") {
                    if tunnel.nodes.isEmpty {
                        // Brutalist empty-state placeholder — see the brand note in
                        // the master plan. Kept plain here.
                        Text("No nodes. Import a subscription to begin.")
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(tunnel.nodes) { node in
                            Button {
                                tunnel.select(node)
                            } label: {
                                HStack {
                                    Text(node.name)
                                    Spacer()
                                    if node.id == tunnel.selectedNode?.id {
                                        Image(systemName: "checkmark")
                                    }
                                }
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .navigationTitle("Tenebra")
        }
    }

    // A toggle whose "on" state maps to connect/disconnect. Reading reflects the
    // live NEVPNStatus; writing kicks off a start or stop. The manager is the
    // source of truth, so the UI never assumes success — it waits for the status
    // to change.
    private var connectBinding: Binding<Bool> {
        Binding(
            get: { tunnel.isActive },
            set: { wantOn in
                Task { wantOn ? await tunnel.connect() : await tunnel.disconnect() }
            }
        )
    }
}
