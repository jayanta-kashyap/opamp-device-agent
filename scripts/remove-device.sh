#!/bin/bash

# Remove Device - Clean Deletion
# Usage: ./scripts/remove-device.sh <device-number>
# Example: ./scripts/remove-device.sh 13

set -e

if [ -z "$1" ]; then
    echo "❌ Error: Device number required"
    echo "Usage: ./scripts/remove-device.sh <device-number>"
    echo "Example: ./scripts/remove-device.sh 13"
    exit 1
fi

DEVICE_NUM=$1
DEVICE_ID="device-$DEVICE_NUM"
NAMESPACE="opamp-edge"
CONTEXT="control-plane"

echo "🗑️  Removing $DEVICE_ID..."

# Delete deployments
echo "Deleting Device-Agent deployment..."
kubectl --context $CONTEXT delete deployment device-agent-$DEVICE_NUM -n $NAMESPACE 2>/dev/null || echo "   (already deleted)"

echo "Deleting Fluent Bit deployment..."
kubectl --context $CONTEXT delete deployment fluentbit-$DEVICE_ID -n $NAMESPACE 2>/dev/null || echo "   (already deleted)"

# Delete service
echo "Deleting Fluent Bit service..."
kubectl --context $CONTEXT delete service fluentbit-$DEVICE_ID -n $NAMESPACE 2>/dev/null || echo "   (already deleted)"

# Delete configmap
echo "Deleting ConfigMap..."
kubectl --context $CONTEXT delete configmap fluentbit-$DEVICE_ID-init-config -n $NAMESPACE 2>/dev/null || echo "   (already deleted)"

# Delete PVC
echo "Deleting PVC..."
kubectl --context $CONTEXT delete pvc $DEVICE_ID-config-pvc -n $NAMESPACE 2>/dev/null || echo "   (already deleted)"

echo ""
echo "✅ $DEVICE_ID removed successfully!"
echo ""
echo "📊 Remaining devices:"
kubectl --context $CONTEXT get pods -n $NAMESPACE | grep -E "NAME|device-agent|fluentbit" || echo "   No devices remaining"
