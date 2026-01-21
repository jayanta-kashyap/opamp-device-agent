// POC Provisioner - Test Device Deployment Service
// This is a POC-only component for deploying test Fluent Bit devices.
// NOT for production use - in production, devices are provisioned differently.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	EdgeNamespace     = "opamp-edge"
	SupervisorService = "opamp-supervisor.opamp-control.svc.cluster.local:50051"
	FluentBitImage    = "fluent/fluent-bit:3.1"
	DeviceAgentImage  = "opamp-device-agent:v1"
)

type Provisioner struct {
	clientset *kubernetes.Clientset
	mu        sync.Mutex
}

type DeployRequest struct {
	DeviceID int `json:"deviceId"`
}

type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

func NewProvisioner() (*Provisioner, error) {
	// Use in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return &Provisioner{clientset: clientset}, nil
}

func (p *Provisioner) DeployDevice(ctx context.Context, deviceID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	deviceName := fmt.Sprintf("device-%d", deviceID)
	log.Printf("Deploying %s...", deviceName)

	// 1. Create PVC
	if err := p.createPVC(ctx, deviceName); err != nil {
		return fmt.Errorf("failed to create PVC: %w", err)
	}

	// 2. Create init ConfigMap
	if err := p.createInitConfigMap(ctx, deviceName); err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	// 3. Create FluentBit deployment
	if err := p.createFluentBitDeployment(ctx, deviceName); err != nil {
		return fmt.Errorf("failed to create FluentBit deployment: %w", err)
	}

	// 4. Create FluentBit service
	if err := p.createFluentBitService(ctx, deviceName); err != nil {
		return fmt.Errorf("failed to create FluentBit service: %w", err)
	}

	// 5. Create DeviceAgent deployment
	if err := p.createDeviceAgentDeployment(ctx, deviceName); err != nil {
		return fmt.Errorf("failed to create DeviceAgent deployment: %w", err)
	}

	log.Printf("Successfully deployed %s", deviceName)
	return nil
}

func (p *Provisioner) RemoveDevice(ctx context.Context, deviceID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	deviceName := fmt.Sprintf("device-%d", deviceID)
	log.Printf("Removing %s...", deviceName)

	// Delete in reverse order
	deletePolicy := metav1.DeletePropagationForeground

	// DeviceAgent deployment
	err := p.clientset.AppsV1().Deployments(EdgeNamespace).Delete(ctx, "device-agent-"+strconv.Itoa(deviceID), metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete device-agent deployment: %v", err)
	}

	// FluentBit deployment
	err = p.clientset.AppsV1().Deployments(EdgeNamespace).Delete(ctx, "fluentbit-"+deviceName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete fluentbit deployment: %v", err)
	}

	// FluentBit service
	err = p.clientset.CoreV1().Services(EdgeNamespace).Delete(ctx, "fluentbit-"+deviceName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete fluentbit service: %v", err)
	}

	// ConfigMap
	err = p.clientset.CoreV1().ConfigMaps(EdgeNamespace).Delete(ctx, "fluentbit-"+deviceName+"-init-config", metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete configmap: %v", err)
	}

	// PVC
	err = p.clientset.CoreV1().PersistentVolumeClaims(EdgeNamespace).Delete(ctx, deviceName+"-config-pvc", metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("Warning: failed to delete PVC: %v", err)
	}

	log.Printf("Successfully removed %s", deviceName)
	return nil
}

func (p *Provisioner) ListDevices(ctx context.Context) ([]string, error) {
	deployments, err := p.clientset.AppsV1().Deployments(EdgeNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "component=device-agent",
	})
	if err != nil {
		return nil, err
	}

	devices := make([]string, 0)
	for _, d := range deployments.Items {
		if deviceID, ok := d.Labels["device-id"]; ok {
			devices = append(devices, deviceID)
		}
	}
	return devices, nil
}

// GetLogs fetches the last few lines of logs from a FluentBit pod
func (p *Provisioner) GetLogs(ctx context.Context, deviceID int, tailLines int64) (string, error) {
	deviceName := fmt.Sprintf("device-%d", deviceID)

	// Find the FluentBit pod
	pods, err := p.clientset.CoreV1().Pods(EdgeNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=fluentbit-%s", deviceName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no fluentbit pod found for %s", deviceName)
	}

	// Get logs from the first running pod
	var targetPod *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			targetPod = &pods.Items[i]
			break
		}
	}

	if targetPod == nil {
		return "", fmt.Errorf("no running fluentbit pod found for %s", deviceName)
	}

	// Fetch logs - use SinceSeconds to get recent logs (last 10 seconds)
	// FluentBit flushes every 5 seconds, so we need a window larger than that
	sinceSeconds := int64(10)
	logOptions := &corev1.PodLogOptions{
		Container:    "fluentbit",
		TailLines:    &tailLines,
		SinceSeconds: &sinceSeconds,
	}

	req := p.clientset.CoreV1().Pods(EdgeNamespace).GetLogs(targetPod.Name, logOptions)
	logStream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get log stream: %w", err)
	}
	defer logStream.Close()

	logs, err := io.ReadAll(logStream)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}

	// Return all logs (both JSON emitted logs and FluentBit system logs)
	// This lets the UI show real pod output whether emission is ON or OFF
	logStr := strings.TrimSpace(string(logs))
	if logStr == "" {
		return "No logs available yet...", nil
	}
	return logStr, nil
}

func (p *Provisioner) createPVC(ctx context.Context, deviceName string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deviceName + "-config-pvc",
			Namespace: EdgeNamespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Mi"),
				},
			},
		},
	}

	_, err := p.clientset.CoreV1().PersistentVolumeClaims(EdgeNamespace).Create(ctx, pvc, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *Provisioner) createInitConfigMap(ctx context.Context, deviceName string) error {
	initConfig := `[SERVICE]
    flush        5
    daemon       Off
    log_level    info
    http_server  On
    http_listen  0.0.0.0
    http_port    2020
    hot_reload   On
`

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fluentbit-" + deviceName + "-init-config",
			Namespace: EdgeNamespace,
		},
		Data: map[string]string{
			"fluent-bit.conf": initConfig,
		},
	}

	_, err := p.clientset.CoreV1().ConfigMaps(EdgeNamespace).Create(ctx, cm, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *Provisioner) createFluentBitDeployment(ctx context.Context, deviceName string) error {
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fluentbit-" + deviceName,
			Namespace: EdgeNamespace,
			Labels: map[string]string{
				"app":       "fluentbit-" + deviceName,
				"device-id": deviceName,
				"component": "fluentbit",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "fluentbit-" + deviceName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       "fluentbit-" + deviceName,
						"device-id": deviceName,
						"component": "fluentbit",
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-config",
							Image: "busybox:1.36",
							Command: []string{"sh", "-c",
								"if [ ! -f /shared-config/fluent-bit.conf ]; then cp /init-config/fluent-bit.conf /shared-config/fluent-bit.conf; fi"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "shared-config", MountPath: "/shared-config"},
								{Name: "init-config", MountPath: "/init-config"},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "fluentbit",
							Image: FluentBitImage,
							Args:  []string{"-c", "/shared-config/fluent-bit.conf"},
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 2020},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "shared-config", MountPath: "/shared-config"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "shared-config",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: deviceName + "-config-pvc",
								},
							},
						},
						{
							Name: "init-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "fluentbit-" + deviceName + "-init-config",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := p.clientset.AppsV1().Deployments(EdgeNamespace).Create(ctx, deployment, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *Provisioner) createFluentBitService(ctx context.Context, deviceName string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fluentbit-" + deviceName,
			Namespace: EdgeNamespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "fluentbit-" + deviceName},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 2020, TargetPort: intstr.FromInt(2020)},
			},
		},
	}

	_, err := p.clientset.CoreV1().Services(EdgeNamespace).Create(ctx, svc, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (p *Provisioner) createDeviceAgentDeployment(ctx context.Context, deviceName string) error {
	replicas := int32(1)
	deviceID := deviceName // e.g., "device-1"

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "device-agent-" + deviceName[7:], // Extract number from "device-X"
			Namespace: EdgeNamespace,
			Labels: map[string]string{
				"app":       "device-agent-" + deviceName[7:],
				"device-id": deviceName,
				"component": "device-agent",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "device-agent-" + deviceName[7:]},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       "device-agent-" + deviceName[7:],
						"device-id": deviceName,
						"component": "device-agent",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "device-agent",
							Image: DeviceAgentImage,
							Args: []string{
								"--node-id=" + deviceID,
								"--supervisor=" + SupervisorService,
								"--agent-type=fluentbit",
								"--config-path=/shared-config/fluent-bit.conf",
								"--reload-endpoint=http://fluentbit-" + deviceName + "." + EdgeNamespace + ".svc.cluster.local:2020/api/v2/reload",
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "shared-config", MountPath: "/shared-config"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "shared-config",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: deviceName + "-config-pvc",
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := p.clientset.AppsV1().Deployments(EdgeNamespace).Create(ctx, deployment, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func main() {
	log.Println("POC Provisioner starting...")
	log.Println("NOTE: This is a POC-only component for testing. Not for production use.")

	provisioner, err := NewProvisioner()
	if err != nil {
		log.Fatalf("Failed to create provisioner: %v", err)
	}

	// Ensure edge namespace exists
	ctx := context.Background()
	_, err = provisioner.clientset.CoreV1().Namespaces().Get(ctx, EdgeNamespace, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: EdgeNamespace}}
		_, err = provisioner.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil {
			log.Fatalf("Failed to create namespace %s: %v", EdgeNamespace, err)
		}
		log.Printf("Created namespace %s", EdgeNamespace)
	}

	mux := http.NewServeMux()

	// CORS middleware
	corsHandler := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			h(w, r)
		}
	}

	// Health check
	mux.HandleFunc("/health", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Deploy device
	mux.HandleFunc("/api/deploy", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req DeployRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(Response{Success: false, Error: "Invalid request"})
			return
		}

		if req.DeviceID < 1 || req.DeviceID > 100 {
			json.NewEncoder(w).Encode(Response{Success: false, Error: "Device ID must be between 1 and 100"})
			return
		}

		if err := provisioner.DeployDevice(r.Context(), req.DeviceID); err != nil {
			json.NewEncoder(w).Encode(Response{Success: false, Error: err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			Success: true,
			Message: fmt.Sprintf("device-%d deployed successfully", req.DeviceID),
		})
	}))

	// Remove device
	mux.HandleFunc("/api/remove", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req DeployRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(Response{Success: false, Error: "Invalid request"})
			return
		}

		if err := provisioner.RemoveDevice(r.Context(), req.DeviceID); err != nil {
			json.NewEncoder(w).Encode(Response{Success: false, Error: err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			Success: true,
			Message: fmt.Sprintf("device-%d removed successfully", req.DeviceID),
		})
	}))

	// List deployed devices
	mux.HandleFunc("/api/devices", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		devices, err := provisioner.ListDevices(r.Context())
		if err != nil {
			json.NewEncoder(w).Encode(Response{Success: false, Error: err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"devices": devices,
		})
	}))

	// Get FluentBit logs for a device
	mux.HandleFunc("/api/logs", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var deviceID int
		var err error

		// Support both GET with query param and POST with body
		if r.Method == http.MethodGet {
			deviceIDStr := r.URL.Query().Get("deviceId")
			if deviceIDStr == "" {
				json.NewEncoder(w).Encode(Response{Success: false, Error: "deviceId required"})
				return
			}
			deviceID, err = strconv.Atoi(deviceIDStr)
			if err != nil {
				json.NewEncoder(w).Encode(Response{Success: false, Error: "Invalid deviceId"})
				return
			}
		} else {
			var req DeployRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				json.NewEncoder(w).Encode(Response{Success: false, Error: "Invalid request"})
				return
			}
			deviceID = req.DeviceID
		}

		logs, err := provisioner.GetLogs(r.Context(), deviceID, 50)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
				"logs":    "",
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"logs":    logs,
		})
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	log.Printf("POC Provisioner listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
