#!/bin/bash

# Remove Multiple Devices - Batch Removal
# Usage: ./scripts/remove-devices-batch.sh <start> <count>
# Example: ./scripts/remove-devices-batch.sh 1 20  (removes device-1 through device-20)

set -e

START=${1:-1}
COUNT=${2:-20}
END=$((START + COUNT - 1))

NAMESPACE="opamp-edge"
CONTEXT="control-plane"

echo "=============================================="
echo "  Batch Device Removal"
echo "=============================================="
echo ""
echo "Removing devices: device-$START through device-$END ($COUNT devices)"
echo ""

# Delete all resources
for i in $(seq $START $END); do
    DEVICE_ID="device-$i"
    echo "🗑️  Removing $DEVICE_ID..."
    
    kubectl --context $CONTEXT delete deployment device-agent-$i -n $NAMESPACE 2>/dev/null &
    kubectl --context $CONTEXT delete deployment fluentbit-$DEVICE_ID -n $NAMESPACE 2>/dev/null &
    kubectl --context $CONTEXT delete service fluentbit-$DEVICE_ID -n $NAMESPACE 2>/dev/null &
    kubectl --context $CONTEXT delete configmap fluentbit-$DEVICE_ID-init-config -n $NAMESPACE 2>/dev/null &
    kubectl --context $CONTEXT delete pvc $DEVICE_ID-config-pvc -n $NAMESPACE 2>/dev/null &
done

echo ""
echo "⏳ Waiting for deletions to complete..."
wait

echo ""
echo "✅ All $COUNT devices removed!"
echo ""
echo "📊 Remaining pods:"
kubectl --context $CONTEXT get pods -n $NAMESPACE 2>/dev/null || echo "   No pods remaining"
