#!/bin/bash

# Add Multiple Devices - Batch Deployment
# Usage: ./scripts/add-devices-batch.sh <start> <count>
# Example: ./scripts/add-devices-batch.sh 1 20  (adds device-1 through device-20)

set -e

START=${1:-1}
COUNT=${2:-20}
END=$((START + COUNT - 1))

NAMESPACE="opamp-edge"
CONTEXT="control-plane"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=============================================="
echo "  Batch Device Deployment"
echo "=============================================="
echo ""
echo "Deploying devices: device-$START through device-$END ($COUNT devices)"
echo ""

# Create all device YAMLs first
echo "📝 Generating deployment manifests..."

COMBINED_YAML="/tmp/all-devices-batch.yaml"
> $COMBINED_YAML  # Clear file

for i in $(seq $START $END); do
    DEVICE_ID="device-$i"
    
    cat >> $COMBINED_YAML <<EOF
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $DEVICE_ID-config-pvc
  namespace: $NAMESPACE
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Mi
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: fluentbit-$DEVICE_ID-init-config
  namespace: $NAMESPACE
data:
  fluent-bit.conf: |
    [SERVICE]
        flush        5
        daemon       Off
        log_level    info
        http_server  On
        http_listen  0.0.0.0
        http_port    2020
        hot_reload   On
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fluentbit-$DEVICE_ID
  namespace: $NAMESPACE
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fluentbit-$DEVICE_ID
  template:
    metadata:
      labels:
        app: fluentbit-$DEVICE_ID
    spec:
      initContainers:
      - name: init-config
        image: busybox:latest
        command: ['sh', '-c', 'if [ ! -f /shared-config/fluent-bit.conf ]; then cp /init-config/fluent-bit.conf /shared-config/fluent-bit.conf && echo "Initial config copied"; else echo "Config exists, preserving OpAMP-managed config"; fi']
        volumeMounts:
        - name: init-config
          mountPath: /init-config
        - name: shared-config
          mountPath: /shared-config
      containers:
      - name: fluentbit
        image: fluent/fluent-bit:3.1
        command: ["/fluent-bit/bin/fluent-bit"]
        args: ["-c", "/shared-config/fluent-bit.conf"]
        ports:
        - containerPort: 2020
          name: http
        volumeMounts:
        - name: shared-config
          mountPath: /shared-config
        resources:
          requests:
            memory: "32Mi"
            cpu: "10m"
          limits:
            memory: "64Mi"
            cpu: "50m"
      volumes:
      - name: init-config
        configMap:
          name: fluentbit-$DEVICE_ID-init-config
      - name: shared-config
        persistentVolumeClaim:
          claimName: $DEVICE_ID-config-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: fluentbit-$DEVICE_ID
  namespace: $NAMESPACE
spec:
  selector:
    app: fluentbit-$DEVICE_ID
  ports:
  - name: http
    port: 2020
    targetPort: 2020
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: device-agent-$i
  namespace: $NAMESPACE
spec:
  replicas: 1
  selector:
    matchLabels:
      app: device-agent-$i
  template:
    metadata:
      labels:
        app: device-agent-$i
    spec:
      containers:
      - name: device-agent
        image: opamp-device-agent:v8
        imagePullPolicy: IfNotPresent
        args:
        - "--supervisor=opamp-supervisor.opamp-control.svc.cluster.local:50051"
        - "--node-id=$DEVICE_ID"
        - "--agent-type=fluentbit"
        - "--config-path=/shared-config/fluent-bit.conf"
        - "--reload-endpoint=http://fluentbit-$DEVICE_ID.opamp-edge.svc.cluster.local:2020/api/v2/reload"
        volumeMounts:
        - name: shared-config
          mountPath: /shared-config
        resources:
          requests:
            memory: "16Mi"
            cpu: "5m"
          limits:
            memory: "32Mi"
            cpu: "20m"
      volumes:
      - name: shared-config
        persistentVolumeClaim:
          claimName: $DEVICE_ID-config-pvc
EOF
    echo "  Generated: $DEVICE_ID"
done

echo ""
echo "🚀 Deploying all devices in one batch..."
kubectl --context $CONTEXT apply -f $COMBINED_YAML

echo ""
echo "⏳ Waiting for deployments to be ready (this may take a few minutes)..."

# Wait for all deployments in parallel
WAIT_TIMEOUT=180
for i in $(seq $START $END); do
    kubectl --context $CONTEXT wait --for=condition=available --timeout=${WAIT_TIMEOUT}s \
        deployment/fluentbit-device-$i deployment/device-agent-$i -n $NAMESPACE 2>/dev/null &
done

# Wait for all background wait commands
wait

echo ""
echo "✅ All $COUNT devices deployed!"
echo ""
echo "📊 Pod Status:"
kubectl --context $CONTEXT get pods -n $NAMESPACE --no-headers | wc -l | xargs -I {} echo "   Total pods: {}"
kubectl --context $CONTEXT get pods -n $NAMESPACE | grep -c "Running" | xargs -I {} echo "   Running: {}" 2>/dev/null || true
echo ""
echo "🌐 Devices should appear in UI at: http://localhost:4321"
echo ""

# Cleanup
rm -f $COMBINED_YAML
