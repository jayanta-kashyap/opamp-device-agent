package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"local.dev/opamp-device-agent/api/controlpb"
)

func main() {
	var (
		supervisorAddr = flag.String("supervisor", "localhost:50051", "Supervisor address")
		opampServerURL = flag.String("opamp-server", "", "OpAMP server URL (e.g., wss://opamp-server:4320/v1/opamp)")
		nodeID         = flag.String("node-id", "", "Node ID (e.g., device-1, device-2)")
		otelConfigPath = flag.String("otel-config", "/etc/otelcol/config.yaml", "Path to OTel Collector config file")
	)
	flag.Parse()

	if *nodeID == "" {
		log.Fatal("--node-id is required")
	}

	agent := NewDeviceAgent(*supervisorAddr, *opampServerURL, *nodeID, *otelConfigPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the agent
	if err := agent.Start(ctx); err != nil {
		log.Fatalf("Failed to start agent: %v", err)
	}

	// Wait for shutdown signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs

	log.Println("Shutting down device agent...")
	agent.Stop()
}

type DeviceAgent struct {
	supervisorAddr string
	opampServerURL string
	nodeID         string
	otelConfigPath string

	conn   *grpc.ClientConn
	client controlpb.ControlServiceClient
	stream controlpb.ControlService_ControlClient

	opampClient   client.OpAMPClient
	configManager *OTelConfigManager
}

func NewDeviceAgent(supervisorAddr, opampServerURL, nodeID, otelConfigPath string) *DeviceAgent {
	return &DeviceAgent{
		supervisorAddr: supervisorAddr,
		opampServerURL: opampServerURL,
		nodeID:         nodeID,
		otelConfigPath: otelConfigPath,
		configManager:  NewOTelConfigManager(otelConfigPath),
	}
}

func (a *DeviceAgent) Start(ctx context.Context) error {
	log.Printf("[Device %s] Connecting to supervisor at %s", a.nodeID, a.supervisorAddr)

	// Connect to supervisor
	conn, err := grpc.NewClient(
		a.supervisorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	a.conn = conn
	a.client = controlpb.NewControlServiceClient(conn)

	// Establish bidirectional stream
	stream, err := a.client.Control(ctx)
	if err != nil {
		return err
	}
	a.stream = stream

	// Send initial registration
	if err := a.register(); err != nil {
		return err
	}

	log.Printf("[Device %s] Connected and registered to supervisor", a.nodeID)

	// Start OpAMP client if URL provided
	if a.opampServerURL != "" {
		if err := a.startOpAMPClient(ctx); err != nil {
			log.Printf("[Device %s] Warning: OpAMP client failed to start: %v", a.nodeID, err)
		}
	}

	// Start receive loop
	go a.receiveLoop(ctx)

	// Start periodic status reports
	go a.statusReportLoop(ctx)

	return nil
}

func (a *DeviceAgent) startOpAMPClient(ctx context.Context) error {
	log.Printf("[Device %s] Connecting to OpAMP server at %s", a.nodeID, a.opampServerURL)

	logger := &simpleLogger{nodeID: a.nodeID}

	settings := types.StartSettings{
		OpAMPServerURL: a.opampServerURL,
		InstanceUid:    types.InstanceUid{},
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				log.Printf("[Device %s] Connected to OpAMP server", a.nodeID)
			},
			OnConnectFailed: func(ctx context.Context, err error) {
				log.Printf("[Device %s] OpAMP connection failed: %v", a.nodeID, err)
			},
			OnError: func(ctx context.Context, err *protobufs.ServerErrorResponse) {
				log.Printf("[Device %s] OpAMP server error: %v", a.nodeID, err.ErrorMessage)
			},
			OnMessage: func(ctx context.Context, msg *types.MessageData) {
				a.onOpAMPMessage(ctx, msg)
			},
		},
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	// Generate instance UID from node ID
	copy(settings.InstanceUid[:], []byte(a.nodeID))

	opampClient := client.NewWebSocket(logger)

	if err := opampClient.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "otel-collector"},
				},
			},
			{
				Key: "device.id",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: a.nodeID},
				},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "device.version",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "1.0.0"},
				},
			},
		},
	}); err != nil {
		return err
	}

	if err := opampClient.Start(ctx, settings); err != nil {
		return err
	}

	a.opampClient = opampClient
	log.Printf("[Device %s] OpAMP client started", a.nodeID)
	return nil
}

func (a *DeviceAgent) onOpAMPMessage(ctx context.Context, msg *types.MessageData) {
	if msg.RemoteConfig != nil {
		log.Printf("[Device %s] Received OpAMP remote config: %d entries", a.nodeID, len(msg.RemoteConfig.Config.ConfigMap))

		for key, cfg := range msg.RemoteConfig.Config.ConfigMap {
			log.Printf("[Device %s] Processing OpAMP config key=%s, body size=%d", a.nodeID, key, len(cfg.Body))

			// Apply the config
			if err := a.configManager.UpdateConfig(string(cfg.Body)); err != nil {
				log.Printf("[Device %s] Failed to apply OpAMP config: %v", a.nodeID, err)
			} else {
				log.Printf("[Device %s] OpAMP config applied successfully", a.nodeID)
			}
		}
	}
}

type simpleLogger struct {
	nodeID string
}

func (l *simpleLogger) Debugf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[OpAMP %s DEBUG] "+format, append([]interface{}{l.nodeID}, v...)...)
}

func (l *simpleLogger) Errorf(ctx context.Context, format string, v ...interface{}) {
	log.Printf("[OpAMP %s ERROR] "+format, append([]interface{}{l.nodeID}, v...)...)
}

func (a *DeviceAgent) register() error {
	reg := &controlpb.EdgeIdentity{
		NodeId:   a.nodeID,
		Version:  "1.0.0",
		Platform: "linux/amd64",
	}

	envelope := &controlpb.Envelope{
		Body: &controlpb.Envelope_Register{
			Register: reg,
		},
	}

	return a.stream.Send(envelope)
}

func (a *DeviceAgent) receiveLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			envelope, err := a.stream.Recv()
			if err != nil {
				log.Printf("[Device %s] Receive error: %v", a.nodeID, err)
				return
			}

			switch body := envelope.Body.(type) {
			case *controlpb.Envelope_Command:
				a.handleCommand(ctx, body.Command)
			default:
				log.Printf("[Device %s] Unknown envelope type", a.nodeID)
			}
		}
	}
}

func (a *DeviceAgent) handleCommand(ctx context.Context, cmd *controlpb.Command) {
	log.Printf("[Device %s] Received command: type=%s, correlationId=%s",
		a.nodeID, cmd.GetType(), cmd.GetCorrelationId())

	switch cmd.GetType() {
	case "UpdateConfig":
		if err := a.configManager.UpdateConfig(cmd.GetPayload()); err != nil {
			log.Printf("[Device %s] Config update failed: %v", a.nodeID, err)
			a.sendEvent(ctx, "ConfigUpdateFailed", err.Error(), cmd.GetCorrelationId())
		} else {
			log.Printf("[Device %s] Config updated successfully", a.nodeID)
			a.sendEvent(ctx, "ConfigUpdateSuccess", "Config applied", cmd.GetCorrelationId())
		}

	case "FetchStatus":
		status := a.configManager.GetStatus()
		a.sendEvent(ctx, "StatusReport", status, cmd.GetCorrelationId())

	case "RestartCollector":
		log.Printf("[Device %s] Restart requested", a.nodeID)
		a.sendEvent(ctx, "RestartAcknowledged", "Restart in progress", cmd.GetCorrelationId())

	default:
		log.Printf("[Device %s] Unknown command type: %s", a.nodeID, cmd.GetType())
	}
}

func (a *DeviceAgent) sendEvent(ctx context.Context, eventType, payload, correlationID string) {
	event := &controlpb.Event{
		Type:          eventType,
		Payload:       payload,
		TsUnixNano:    time.Now().UnixNano(),
		CorrelationId: correlationID,
	}

	envelope := &controlpb.Envelope{
		Body: &controlpb.Envelope_Event{
			Event: event,
		},
	}

	if err := a.stream.Send(envelope); err != nil {
		log.Printf("[Device %s] Failed to send event: %v", a.nodeID, err)
	}
}

func (a *DeviceAgent) statusReportLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status := a.configManager.GetStatus()
			a.sendEvent(ctx, "PeriodicStatus", status, "")
		}
	}
}

func (a *DeviceAgent) Stop() {
	if a.stream != nil {
		a.stream.CloseSend()
	}
	if a.conn != nil {
		a.conn.Close()
	}
}
