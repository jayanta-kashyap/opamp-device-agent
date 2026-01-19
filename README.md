# Device Agent - Edge Device Controller

## What is this?

The **Device Agent** manages Fluent Bit on edge devices. It connects to the OpAMP Supervisor in the cloud and receives configuration updates. When a new config arrives, it writes the config to a shared storage location where Fluent Bit can read it.

Think of it as the **remote control receiver** - it listens for commands from the cloud and applies them locally.

---

## 🎯 Architecture: Separate Pods, Shared Storage

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              One Edge Device                                │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │                          Device-Agent Pod                             │ │
│  │                                                                       │ │
│  │  • Connects to Supervisor via gRPC                                   │ │
│  │  • Receives config updates                                           │ │
│  │  • Writes to /shared-config/fluent-bit.conf                          │ │
│  │  • Sends status back every 30s                                       │ │
│  │  • Queries Fluent Bit runtime state                                  │ │
│  └───────────────────────┬───────────────────────────────────────────────┘ │
│                          │                                                  │
│                          │ Shared PVC (ReadWriteMany)                       │
│                          │ Mounted at: /shared-config                       │
│                          │                                                  │
│  ┌───────────────────────▼───────────────────────────────────────────────┐ │
│  │                        Fluent Bit Pod                                 │ │
│  │                                                                       │ │
│  │  • Reads from /shared-config/fluent-bit.conf                         │ │
│  │  • Hot reload API on port 2020                                       │ │
│  │  • Automatically reloads when config changes                         │ │
│  │  • Emits logs to stdout                                              │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Why Separate Pods?

**1. Hot Reload Without Restart**
   - Device-Agent writes new config → Shared PVC
   - Fluent Bit reads from same PVC
   - Fluent Bit detects change via hot reload API
   - **No pod restart needed** = zero downtime
   - Fluent Bit keeps emitting logs while config updates

**2. Isolation = Stability**
   - If Device-Agent crashes → Fluent Bit keeps running
   - If Fluent Bit crashes → Device-Agent stays connected to cloud
   - Each pod can restart independently
   - Updates to one don't affect the other

**3. Shared Storage (PVC)**
   - Both pods mount the same volume
   - Device-Agent: Writes to `/shared-config/fluent-bit.conf`
   - Fluent Bit: Reads from `/shared-config/fluent-bit.conf`
   - Uses `ReadWriteMany` so both can access simultaneously
   - Changes are visible instantly to both pods

---

## 🔄 Config Update Flow

```
1. User clicks "Enable Emission" in UI
         │
         ▼
2. OpAMP Server → OpAMP Supervisor (cloud)
         │
         ▼
3. OpAMP Supervisor → Device-Agent (gRPC)
         │
         ▼
4. Device-Agent writes to /shared-config/fluent-bit.conf
         │
         ▼
5. Device-Agent calls Fluent Bit reload API
         │   http://fluentbit-device-X:2020/api/v2/reload
         │
         ▼
6. Fluent Bit detects config change
         │
         ▼
7. Fluent Bit hot reloads (no restart)
         │
         ▼
8. Fluent Bit starts emitting logs ✅
         │
         ▼
9. Device-Agent sends status back to cloud
```

---

## ✨ Current Features

| Feature | Description | Status |
|---------|-------------|--------|
| **gRPC Client** | Connects to OpAMP Supervisor | ✅ Working |
| **Config Management** | Writes Fluent Bit configs to PVC | ✅ Working |
| **Hot Reload** | Calls Fluent Bit reload API | ✅ Working |
| **Runtime Monitoring** | Queries Fluent Bit state every 30s | ✅ Working |
| **Heartbeat** | Sends status to cloud regularly | ✅ Working |
| **Auto-Reconnect** | Reconnects if connection drops | ✅ Working |
| **File Fallback** | Reads config from file if API fails | ✅ Working |

---

## 🔧 How Configs Work

### Default Config (Emission OFF)
```ini
[SERVICE]
    flush        5
    daemon       Off
    log_level    info

# No INPUT or OUTPUT sections = silent mode
```

### Active Config (Emission ON)
```ini
[SERVICE]
    flush        5
    daemon       Off
    log_level    info
    http_server  On
    http_listen  0.0.0.0
    http_port    2020
    hot_reload   On

[INPUT]
    name         dummy
    tag          logs
    dummy        {"message":"test log","level":"info"}
    rate         1

[OUTPUT]
    name         stdout
    match        *
    format       json_lines
```

When emission is enabled:
- Device-Agent receives config from cloud
- Writes it to `/shared-config/fluent-bit.conf`
- Calls reload API
- Fluent Bit starts generating dummy logs at 1/sec
- Logs appear in Fluent Bit pod output

---

## 🚀 One-Command Deployment (Plug & Play)

### Add a New Device
```bash
./scripts/add-device.sh 13
```

That's it! The script automatically:
- ✅ Generates Fluent Bit deployment
- ✅ Generates Device-Agent deployment  
- ✅ Creates shared PVC (ReadWriteMany)
- ✅ Deploys both pods
- ✅ Device auto-connects to supervisor
- ✅ Appears in UI within seconds

### Remove a Device
```bash
./scripts/remove-device.sh 13
```

Cleanly removes:
- Device-Agent deployment
- Fluent Bit deployment
- Service
- ConfigMap
- PVC

### What Happens Automatically?

```
1. You run: ./scripts/add-device.sh 13
         │
         ▼
2. Script creates PVC + Fluent Bit + Device-Agent
         │
         ▼
3. Device-Agent connects to Supervisor
         │
         ▼
4. Supervisor auto-registers device
         │
         ▼
5. Supervisor reports to OpAMP Server
         │
         ▼
6. Device appears in UI ✅
```

No manual configuration needed!

---

## 🚀 Deployment

### Using Setup Script (Recommended)
```bash
# From opamp-server directory - deploys everything including devices
cd ../opamp-server
./scripts/setup.sh
```

### Adding/Removing Devices Manually
```bash
# Add a device (creates all resources dynamically)
./scripts/add-device.sh 5

# Remove a device
./scripts/remove-device.sh 5
```

### Build Image Only
```bash
eval $(minikube -p control-plane docker-env)
docker build -t opamp-device-agent:v8 .
```

### Verify Deployment
```bash
# Check status
kubectl --context control-plane get pods -n opamp-edge

# Check device-agent logs
kubectl --context control-plane logs -n opamp-edge -l app=device-agent-3

# Check fluent bit logs (when emission ON)
kubectl --context control-plane logs -n opamp-edge -l app=fluentbit-device-3
```

---

## 📁 Project Structure

```
opamp-device-agent/
├── main.go                     # Main entry point
├── scripts/
│   ├── add-device.sh           # Dynamically create and deploy devices
│   └── remove-device.sh        # Remove devices and cleanup
├── k8s/                        # (empty - devices created dynamically by scripts)
└── configs/                    # Config templates
```

---

## 🔑 Key Configuration

Each device needs:

1. **Device-Agent Deployment**
   - Environment: `DEVICE_ID=device-3`
   - Environment: `SUPERVISOR_ADDR=opamp-supervisor.opamp-control.svc.cluster.local:50051`
   - Volume mount: `/shared-config` (PVC)

2. **Fluent Bit Deployment**
   - Volume mount: `/shared-config` (same PVC)
   - HTTP server: Port 2020 for hot reload API
   - Config path: `/shared-config/fluent-bit.conf`

3. **PVC (Persistent Volume Claim)**
   - Access mode: `ReadWriteMany`
   - Size: `10Mi`
   - Shared between both pods

---

## 🐛 Troubleshooting

### Device not appearing in UI?
```bash
# Check if device-agent is connected
kubectl --context control-plane logs -n opamp-edge -l app=device-agent-X | grep "Connected"

# Check supervisor logs
kubectl --context control-plane logs -n opamp-control -l app=opamp-supervisor | grep "device-X"
```

### Toggle not working?
```bash
# Check device-agent received config
kubectl --context control-plane logs -n opamp-edge -l app=device-agent-X | grep "ConfigPush"

# Check if reload API was called
kubectl --context control-plane logs -n opamp-edge -l app=device-agent-X | grep "reload API"

# Check Fluent Bit actually started emitting
kubectl --context control-plane logs -n opamp-edge -l app=fluentbit-device-X --tail=10
```

### PVC mount issues?
```bash
# Check PVC status
kubectl --context control-plane get pvc -n opamp-edge | grep device-X

# Verify both pods using same PVC
kubectl --context control-plane describe pod <device-agent-pod> -n opamp-edge | grep -A5 "Volumes"
kubectl --context control-plane describe pod <fluentbit-pod> -n opamp-edge | grep -A5 "Volumes"
```
