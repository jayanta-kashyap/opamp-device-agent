# Device Agent (Edge OTel Agent Manager)

## Overview
The Device Agent runs on edge devices and acts as a **gRPC client** that connects to the Supervisor. It manages a local OpenTelemetry Collector instance, receiving configuration updates via gRPC and applying them to the collector in real-time.

## Role in the POC Architecture

```
        Cloud Layer (Minikube)
┌─────────────────────────────────────────┐
│          OpAMP Server                   │
└─────────────────────────────────────────┘
                    ↕
           OpAMP Protocol
                    ↕
┌─────────────────────────────────────────┐
│           Supervisor                    │
│         (gRPC Server)                   │
└─────────────────────────────────────────┘
                    ↕
        Bidirectional gRPC Stream
                    ↕
┌─────────────────────────────────────────┐
│  Device Agent (This Component)          │
│  ┌────────────────────────────────┐    │
│  │     gRPC Client                │    │
│  │  Connects to Supervisor:50051  │    │
│  └────────────────────────────────┘    │
│                 ↕                       │
│  ┌────────────────────────────────┐    │
│  │  OTel Config Manager           │    │
│  │  (Writes config to disk)       │    │
│  └────────────────────────────────┘    │
└─────────────────────────────────────────┘
                    ↕
          Reads config file
                    ↕
┌─────────────────────────────────────────┐
│    OpenTelemetry Collector              │
│    (Separate Container/Process)         │
│  - Receives telemetry (OTLP)           │
│  - Processes with pipelines            │
│  - Exports to backends                 │
└─────────────────────────────────────────┘
```

## Components

### 1. gRPC Client
**Purpose:** Establishes and maintains bidirectional streaming connection to Supervisor.

**Functionality:**
- **Connection Establishment:** Connects to supervisor at startup
- **Device Registration:** Sends initial "Connected" event with device ID
- **Command Reception:** Listens for incoming commands (configs, control)
- **Status Reporting:** Sends events back to supervisor (success, errors, status)
- **Reconnection:** Handles disconnections with exponential backoff

**How it works:**
```go
// Connect to supervisor
conn, _ := grpc.Dial("supervisor:50051")
client := controlpb.NewControlServiceClient(conn)

// Open bidirectional stream
stream, _ := client.Stream(context.Background())

// Send initial registration
stream.Send(&controlpb.Event{
    Type: "Connected",
    Payload: `{"deviceId": "device-1"}`,
})

// Listen for commands
for {
    cmd, _ := stream.Recv()
    handleCommand(cmd)
}
```

**Command Handling:**
- `UpdateConfig`: Receives YAML config, writes to disk, triggers reload
- `FetchStatus`: Returns current device/collector status
- `RestartCollector`: Restarts the OTel Collector process

### 2. OTel Config Manager
**Purpose:** Manages the OpenTelemetry Collector configuration file and lifecycle.

**Functionality:**
- **Config Writing:** Receives YAML configs and writes to disk
- **File Management:** Ensures proper permissions and atomic writes
- **Change Detection:** Only updates if config actually changed
- **Reload Signaling:** Triggers collector to reload configuration
- **Validation:** Basic YAML validation before applying

**File Path:** `/etc/otelcol/config.yaml`

**How it works:**
```go
type ConfigManager struct {
    configPath string
}

func (m *ConfigManager) UpdateConfig(yamlContent []byte) error {
    // Write to temporary file first
    tmpFile := m.configPath + ".tmp"
    ioutil.WriteFile(tmpFile, yamlContent, 0644)
    
    // Validate YAML
    if !isValidYAML(yamlContent) {
        return errors.New("invalid YAML")
    }
    
    // Atomic rename
    os.Rename(tmpFile, m.configPath)
    
    // Signal collector to reload
    m.signalReload()
    
    return nil
}
```

**Reload Mechanisms:**
1. **File Watch:** OTel Collector watches config file for changes
2. **SIGHUP Signal:** Send signal to collector process (if supported)
3. **HTTP API:** Call collector's reload endpoint (if enabled)

### 3. OpAMP Client (Optional)
**Purpose:** Direct connection to OpAMP Server for redundancy and status reporting.

**Functionality:**
- **Direct Registration:** Can register with OpAMP Server independently
- **Status Reporting:** Sends device metrics and health directly
- **Fallback Path:** Provides redundancy if supervisor connection fails
- **Capability Negotiation:** Reports device capabilities to server

**Note:** In the current POC, this primarily serves as a status reporter while supervisor handles config distribution.

## Message Flow Examples

### Startup Flow
1. Device agent starts with args:
   ```bash
   --supervisor=supervisor:50051
   --node-id=device-1
   --otel-config=/etc/otelcol/config.yaml
   ```
2. Opens gRPC stream to supervisor
3. Sends "Connected" event:
   ```json
   {
     "type": "Connected",
     "payload": "{\"deviceId\": \"device-1\", \"version\": \"1.0.0\"}",
     "correlation_id": "startup-001"
   }
   ```
4. Waits for commands from supervisor

### Configuration Update Flow
1. Supervisor sends UpdateConfig command:
   ```protobuf
   Command {
     type: "UpdateConfig"
     payload: "receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: 0.0.0.0:4317"
     correlation_id: "config-update-123"
   }
   ```
2. Device agent receives command
3. Config manager validates YAML
4. Config manager writes to `/etc/otelcol/config.yaml`
5. Config manager signals OTel Collector to reload
6. Device agent sends success event:
   ```json
   {
     "type": "ConfigApplied",
     "payload": "{\"status\": \"success\", \"lines\": 45}",
     "correlation_id": "config-update-123"
   }
   ```

### Error Handling Flow
1. Invalid config received
2. Config manager detects YAML error
3. Device agent sends error event:
   ```json
   {
     "type": "ConfigError",
     "payload": "{\"error\": \"invalid YAML: line 5\"}",
     "correlation_id": "config-update-124"
   }
   ```
4. Previous valid config remains active
5. Supervisor can retry or notify operator

### Periodic Status Flow
1. Device agent monitors OTel Collector health
2. Every 30 seconds, sends status event:
   ```json
   {
     "type": "Status",
     "payload": "{\"collector\": \"running\", \"memory\": \"45MB\", \"uptime\": \"2h15m\"}"
   }
   ```
3. Supervisor forwards to OpAMP Server
4. OpAMP Server updates device status in UI

## OTel Collector Integration

The Device Agent **manages** but doesn't **contain** the OTel Collector:

```
Device Agent Container
├── main.go (gRPC client)
├── otel_config_manager.go
└── /etc/otelcol/config.yaml (shared volume)

OTel Collector Container
├── otelcol (collector binary)
└── /etc/otelcol/config.yaml (watches this file)
```

**Shared Volume:** Both containers mount `/etc/otelcol` so:
- Device agent writes `config.yaml`
- OTel collector reads `config.yaml`
- Collector auto-reloads on file change

**Kubernetes Deployment:**
```yaml
Pod: device-agent-1
  - Container: device-agent
    - Mounts: config-volume at /etc/otelcol
  - Container: otel-collector
    - Mounts: config-volume at /etc/otelcol
Volumes:
  - config-volume (emptyDir)
```

## Command-Line Arguments

```bash
device-agent \
  --supervisor=supervisor.opamp-system.svc.cluster.local:50051 \
  --node-id=device-1 \
  --otel-config=/etc/otelcol/config.yaml
```

**Arguments:**
- `--supervisor`: gRPC address of supervisor
- `--node-id`: Unique identifier for this device
- `--otel-config`: Path where OTel config should be written

## Configuration Templates

Pre-built configs in `configs/` directory:

### device-1-otel-config.yaml
```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:
    timeout: 10s

exporters:
  logging:
    loglevel: debug

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]
```

### enable-logs-pipeline.yaml
```yaml
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [logging]
```

## Key Features in POC

1. **Remote Management:** Accepts configs from anywhere via supervisor
2. **Hot Reload:** Applies configs without restarting collector
3. **Error Recovery:** Maintains last valid config on errors
4. **Status Visibility:** Reports health upstream
5. **Lightweight:** Minimal resource footprint
6. **Stateless:** No local database or persistence needed

## Technology Stack
- **Language:** Go
- **gRPC:** google.golang.org/grpc
- **Protocol:** Defined in shared control.proto
- **OTel:** Manages otel/opentelemetry-collector

## Deployment
- **Container Image:** `device-agent:latest`
- **Kubernetes:** Deployed in `opamp-system` namespace
- **Replicas:** Multiple instances (device-1, device-2, ...)
- **Volumes:** Shared emptyDir for config file

## Building
```bash
# Build binary
go build -o device-agent .

# Build Docker image
docker build -t device-agent:latest -f Dockerfile .
```

## Directory Structure
```
opamp-device-agent/
├── main.go                    # gRPC client and main loop
├── otel_config_manager.go     # Config file management
├── api/
│   ├── control.proto          # Shared with supervisor
│   └── controlpb/             # Generated code
├── configs/                   # Example configs
│   ├── device-1-otel-config.yaml
│   ├── device-2-otel-config.yaml
│   ├── enable-logs-pipeline.yaml
│   └── enable-traces-pipeline.yaml
└── k8s/
    ├── device-agent.yaml      # Device deployments
    └── otel-collector.yaml    # OTel collector deployments
```

## How This Enables E2E POC

The Device Agent is the **edge component** that completes the POC:

1. **Edge Presence:** Represents actual devices in the field
2. **gRPC Client:** Demonstrates edge-to-cloud communication
3. **Config Application:** Shows real config changes taking effect
4. **OTel Management:** Proves remote management of telemetry pipeline
5. **Bidirectional Comms:** Reports status back to cloud
6. **Real-world Simulation:** Mimics actual edge deployment

**E2E Flow Completion:**
```
UI Input (OpAMP Server)
    ↓
OpAMP Protocol (OpAMP Server → Supervisor)
    ↓
gRPC Translation (Supervisor → Device Agent)
    ↓
Config Application (Device Agent → OTel Collector)
    ↓
Telemetry Collection (OTel Collector)
    ↓
Status Feedback (Device Agent → Supervisor → OpAMP Server → UI)
```

**Without the device agent:**
- No edge component to manage
- No way to demonstrate config changes
- No proof of remote management working
- No telemetry pipeline to control
- POC would be incomplete

The device agent proves that **centralized management of distributed OTel collectors is possible and practical**.

## Testing Locally

1. Start supervisor in one terminal:
   ```bash
   cd opamp-poc-supervisor
   go run ./cmd/supervisor
   ```

2. Start device agent in another:
   ```bash
   cd opamp-device-agent
   go run . --supervisor=localhost:50051 --node-id=device-local
   ```

3. Watch logs to see gRPC connection and config updates

## Production Considerations

For real deployments:
- Add TLS for gRPC connections
- Implement authentication/authorization
- Add config validation/schema checks
- Include collector process monitoring
- Add retry logic with exponential backoff
- Implement graceful shutdown
- Add metrics and health endpoints
- Use persistent volumes for critical state
