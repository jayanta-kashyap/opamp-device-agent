#!/bin/bash

# Add Device - One Command Deployment
# Usage: ./scripts/add-device.sh <device-number>
# Example: ./scripts/add-device.sh 13

set -e

if [ -z "$1" ]; then
    echo "❌ Error: Device number required"
    echo "Usage: ./scripts/add-device.sh <device-number>"
    echo "Example: ./scripts/add-device.sh 13"
    exit 1
fi

DEVICE_NUM=$1
DEVICE_ID="device-$DEVICE_NUM"
NAMESPACE="opamp-edge"
CONTEXT="control-plane"

echo "🚀 Deploying $DEVICE_ID..."

# Create temporary YAML files
FLUENTBIT_YAML="/tmp/fluentbit-$DEVICE_ID.yaml"
AGENT_YAML="/tmp/device-agent-$DEVICE_ID.yaml"

# Generate Fluent Bit deployment
cat > $FLUENTBIT_YAML <<EOF
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
        command: ['sh', '-c', 'cp /init-config/fluent-bit.conf /shared-config/fluent-bit.conf && echo "Initial config copied"']
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
EOF

# Generate Device-Agent deployment
cat > $AGENT_YAML <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: device-agent-$DEVICE_NUM
  namespace: $NAMESPACE
spec:
  replicas: 1
  selector:
    matchLabels:
      app: device-agent-$DEVICE_NUM
  template:
    metadata:
      labels:
        app: device-agent-$DEVICE_NUM
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
      volumes:
      - name: shared-config
        persistentVolumeClaim:
          claimName: $DEVICE_ID-config-pvc
EOF

# Deploy to Kubernetes
echo "📦 Creating PVC and deploying Fluent Bit..."
kubectl --context $CONTEXT apply -f $FLUENTBIT_YAML

echo "⏳ Waiting for Fluent Bit to be ready..."
kubectl --context $CONTEXT wait --for=condition=available --timeout=60s deployment/fluentbit-$DEVICE_ID -n $NAMESPACE 2>/dev/null || true

echo "📦 Deploying Device-Agent..."
kubectl --context $CONTEXT apply -f $AGENT_YAML

echo "⏳ Waiting for Device-Agent to be ready..."
kubectl --context $CONTEXT wait --for=condition=available --timeout=60s deployment/device-agent-$DEVICE_NUM -n $NAMESPACE 2>/dev/null || true

# Cleanup temp files
rm -f $FLUENTBIT_YAML $AGENT_YAML

echo ""
echo "✅ $DEVICE_ID deployed successfully!"
echo ""
echo "📊 Pod Status:"
kubectl --context $CONTEXT get pods -n $NAMESPACE | grep -E "NAME|$DEVICE_ID|device-agent-$DEVICE_NUM"
echo ""
echo "🌐 Device should appear in UI at: http://localhost:8080"
echo ""
echo "📝 To check logs:"
echo "   Device-Agent: kubectl --context $CONTEXT logs -n $NAMESPACE -l app=device-agent-$DEVICE_NUM -f"
echo "   Fluent Bit:   kubectl --context $CONTEXT logs -n $NAMESPACE -l app=fluentbit-$DEVICE_ID -f"
echo ""
echo "🗑️  To remove: ./scripts/remove-device.sh $DEVICE_NUM"
