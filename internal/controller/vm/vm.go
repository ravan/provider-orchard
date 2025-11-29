/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	v1alpha1 "github.com/ravan/provider-orchard/apis/compute/v1alpha1"
	apisv1alpha1 "github.com/ravan/provider-orchard/apis/v1alpha1"
	orchardclient "github.com/ravan/provider-orchard/internal/clients/orchard"
	"github.com/ravan/provider-orchard/internal/ssh"
)

const (
	errNotVM        = "managed resource is not a VM custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCPC       = "cannot get ClusterProviderConfig"
	errGetCreds     = "cannot get credentials"

	errNewClient       = "cannot create new Orchard client"
	errGetVM           = "cannot get VM"
	errCreateVM        = "cannot create VM"
	errUpdateVM        = "cannot update VM"
	errDeleteVM        = "cannot delete VM"
	errParseToken      = "cannot parse token from credentials"
	errExecuteCloudInit = "cannot execute cloud-init script"
	errSSHNotReady     = "SSH not ready"

	// Cloud-init status values
	CloudInitStatusPending   = "pending"
	CloudInitStatusRunning   = "running"
	CloudInitStatusCompleted = "completed"
	CloudInitStatusFailed    = "failed"

	// Default SSH credentials
	DefaultSSHUsername = "admin"
	DefaultSSHPassword = "admin"

	// Maximum length for condition messages (for readability)
	maxConditionMessageLen = 1024
)

// SetupGated adds a controller that reconciles VM managed resources with safe-start support.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			panic(errors.Wrap(err, "cannot setup VM controller"))
		}
	}, v1alpha1.VMGroupVersionKind)
	return nil
}

func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.VMGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnector(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}

	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		opts = append(opts, managed.WithManagementPolicies())
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.VMList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.VMList")
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.VMGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.VM{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube  client.Client
	usage *resource.ProviderConfigUsageTracker
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.VM)
	if !ok {
		return nil, errors.New(errNotVM)
	}

	if err := c.usage.Track(ctx, cr); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// Get provider config and base URL
	m := mg.(resource.ModernManaged)
	cd, baseURL, err := c.getProviderConfigAndBaseURL(ctx, m)
	if err != nil {
		return nil, err
	}

	// Extract credentials
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	// Extract token from credentials
	token, err := extractToken(data)
	if err != nil {
		return nil, err
	}

	// Create Orchard client
	orchardClient, err := orchardclient.NewOrchardClient(orchardclient.OrchardConfig{
		BaseURL: baseURL,
		Token:   token,
	})
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{
		client:  orchardClient,
		baseURL: baseURL,
		token:   token,
	}, nil
}

// getProviderConfigAndBaseURL retrieves provider credentials and base URL from ProviderConfig or ClusterProviderConfig
func (c *connector) getProviderConfigAndBaseURL(ctx context.Context, m resource.ModernManaged) (apisv1alpha1.ProviderCredentials, string, error) {
	ref := m.GetProviderConfigReference()

	switch ref.Kind {
	case "ProviderConfig":
		pc := &apisv1alpha1.ProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: m.GetNamespace()}, pc); err != nil {
			return apisv1alpha1.ProviderCredentials{}, "", errors.Wrap(err, errGetPC)
		}
		return pc.Spec.Credentials, pc.Spec.BaseURL, nil
	case "ClusterProviderConfig":
		cpc := &apisv1alpha1.ClusterProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, cpc); err != nil {
			return apisv1alpha1.ProviderCredentials{}, "", errors.Wrap(err, errGetCPC)
		}
		return cpc.Spec.Credentials, cpc.Spec.BaseURL, nil
	default:
		return apisv1alpha1.ProviderCredentials{}, "", errors.Errorf("unsupported provider config kind: %s", ref.Kind)
	}
}

// extractToken parses and extracts the token from credential data
func extractToken(data []byte) (string, error) {
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", errors.Wrap(err, errParseToken)
	}

	token, ok := creds["token"]
	if !ok {
		return "", errors.New(errParseToken)
	}

	return token, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	client  *orchardclient.OrchardClient
	baseURL string // Orchard base URL for SSH tunnel
	token   string // Bearer token for SSH tunnel
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.VM)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotVM)
	}

	// Use the VM name as the external name (unique identifier in Orchard)
	vmName := meta.GetExternalName(cr)
	if vmName == "" {
		// If no external name is set, the VM hasn't been created yet
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// Get VM from Orchard API
	resp, err := c.client.GetVmsName(ctx, vmName, nil)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetVM)
	}

	return c.handleObserveResponse(ctx, resp, cr, vmName)
}

// handleObserveResponse processes the API response and updates the CR status
func (c *external) handleObserveResponse(ctx context.Context, resp *http.Response, cr *v1alpha1.VM, vmName string) (managed.ExternalObservation, error) {
	switch resp.StatusCode {
	case http.StatusNotFound:
		// VM doesn't exist
		return managed.ExternalObservation{ResourceExists: false}, nil
	case http.StatusOK:
		// VM exists, parse response
		var vm orchardclient.VM
		if err := json.NewDecoder(resp.Body).Decode(&vm); err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, "cannot decode VM response")
		}
		defer resp.Body.Close()

		// Update observation fields
		updateVMStatus(cr, &vm)

		// Fetch IP address if VM is running
		if stringValue(vm.Status) == "running" {
			if ipResp, err := c.client.GetVmsNameIp(ctx, vmName, nil); err == nil && ipResp.StatusCode == http.StatusOK {
				var ip orchardclient.IP
				if err := json.NewDecoder(ipResp.Body).Decode(&ip); err == nil {
					if ip.Ip != nil {
						cr.Status.AtProvider.IPAddress = *ip.Ip
					}
				}
				ipResp.Body.Close()
			}

			// Handle cloud-init execution if VM is running with IP
			if cr.Status.AtProvider.IPAddress != "" {
				if err := c.handleCloudInit(ctx, cr); err != nil {
					// Transient error (SSH not ready) - will retry on next reconcile
					return managed.ExternalObservation{
						ResourceExists:   true,
						ResourceUpToDate: true,
					}, errors.Wrap(err, errExecuteCloudInit)
				}
			} else {
				// No IP yet - stay in Creating state
				cr.SetConditions(xpv1.Creating())
			}
		}

		// Check if resource is up to date
		upToDate := isVMUpToDate(&cr.Spec.ForProvider, &vm)

		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: upToDate,
		}, nil
	default:
		return managed.ExternalObservation{}, errors.Errorf("unexpected status code: %d", resp.StatusCode)
	}
}

// updateVMStatus updates the CR status fields from the VM response
func updateVMStatus(cr *v1alpha1.VM, vm *orchardclient.VM) {
	cr.Status.AtProvider.Status = stringValue(vm.Status)
	if vm.StatusMessage != nil {
		cr.Status.AtProvider.StatusMessage = *vm.StatusMessage
	}
	if vm.Worker != nil {
		cr.Status.AtProvider.Worker = *vm.Worker
	}
	if vm.Generation != nil {
		gen := int32(*vm.Generation)
		cr.Status.AtProvider.Generation = &gen
	}
	if vm.ObservedGeneration != nil {
		obsGen := int32(*vm.ObservedGeneration)
		cr.Status.AtProvider.ObservedGeneration = &obsGen
	}

	// Set Ready condition based on VM status
	// Note: "running" state is handled in handleObserveResponse after cloud-init check
	vmStatus := stringValue(vm.Status)
	switch vmStatus {
	case "running":
		// Don't set Available here - handleCloudInit will set the appropriate condition
		// after checking/executing cloud-init scripts
		cr.SetConditions(xpv1.Creating())
	case "creating", "starting", "pending":
		cr.SetConditions(xpv1.Creating())
	case "stopping", "deleting":
		cr.SetConditions(xpv1.Deleting())
	case "failed", "error":
		cr.SetConditions(xpv1.Unavailable())
	}
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.VM)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotVM)
	}

	// Use the Kubernetes resource name as the VM name
	vmName := cr.Name
	meta.SetExternalName(cr, vmName)

	// Build VM spec from parameters
	vmSpec := buildVMSpec(&cr.Spec.ForProvider)

	// Create VM via Orchard API
	// NOTE: StartupScript is intentionally NOT included - it causes VM crashes.
	// Cloud-init scripts are executed via SSH after the VM is running.
	createReq := orchardclient.PostVmsJSONRequestBody{
		Cpu:             vmSpec.Cpu,
		DiskSize:        vmSpec.DiskSize,
		Headless:        vmSpec.Headless,
		HostDirs:        vmSpec.HostDirs,
		Image:           vmSpec.Image,
		Labels:          vmSpec.Labels,
		Memory:          vmSpec.Memory,
		Name:            &vmName,
		Nested:          vmSpec.Nested,
		NetBridged:      vmSpec.NetBridged,
		NetSoftnet:      vmSpec.NetSoftnet,
		NetSoftnetAllow: vmSpec.NetSoftnetAllow,
		NetSoftnetBlock: vmSpec.NetSoftnetBlock,
		Password:        vmSpec.Password,
		Resources:       vmSpec.Resources,
		Suspendable:     vmSpec.Suspendable,
		Username:        vmSpec.Username,
	}

	if vmSpec.ImagePullPolicy != nil {
		policy := orchardclient.PostVmsJSONBodyImagePullPolicy(*vmSpec.ImagePullPolicy)
		createReq.ImagePullPolicy = &policy
	}

	if vmSpec.RestartPolicy != nil {
		policy := orchardclient.PostVmsJSONBodyRestartPolicy(*vmSpec.RestartPolicy)
		createReq.RestartPolicy = &policy
	}

	resp, err := c.client.PostVms(ctx, createReq)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateVM)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return managed.ExternalCreation{}, errors.Errorf("unexpected status code creating VM: %d", resp.StatusCode)
	}

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.VM)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotVM)
	}

	vmName := meta.GetExternalName(cr)
	if vmName == "" {
		return managed.ExternalUpdate{}, errors.New("external name not set")
	}

	// Build VM spec from parameters
	vmSpec := buildVMSpec(&cr.Spec.ForProvider)

	// Update VM via Orchard API
	resp, err := c.client.PutVmsName(ctx, vmName, nil, *vmSpec)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateVM)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return managed.ExternalUpdate{}, errors.Errorf("unexpected status code updating VM: %d", resp.StatusCode)
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.VM)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotVM)
	}

	vmName := meta.GetExternalName(cr)
	if vmName == "" {
		// Nothing to delete
		return managed.ExternalDelete{}, nil
	}

	// Delete VM via Orchard API
	resp, err := c.client.DeleteVmsName(ctx, vmName, nil)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, errDeleteVM)
	}
	defer resp.Body.Close()

	// 404 is acceptable - VM is already deleted
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return managed.ExternalDelete{}, errors.Errorf("unexpected status code deleting VM: %d", resp.StatusCode)
	}

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

// Helper functions

func buildVMSpec(params *v1alpha1.VMParameters) *orchardclient.VMSpec {
	spec := &orchardclient.VMSpec{
		Image: &params.Image,
	}

	if params.CPU != nil {
		cpu := float32(*params.CPU)
		spec.Cpu = &cpu
	}

	if params.Memory != nil {
		mem := float32(*params.Memory)
		spec.Memory = &mem
	}

	if params.DiskSize != nil {
		disk := float32(*params.DiskSize)
		spec.DiskSize = &disk
	}

	// NOTE: StartupScript is NOT passed to Orchard API because it causes VM crashes.
	// Instead, cloud-init scripts are executed via SSH after the VM is running.
	// See handleCloudInit() and executeCloudInit() for the SSH-based implementation.

	spec.Username = params.Username
	spec.Password = params.Password
	spec.Headless = params.Headless
	spec.NetBridged = params.NetBridged
	spec.NetSoftnet = params.NetSoftnet
	spec.Nested = params.Nested
	spec.Suspendable = params.Suspendable

	if len(params.NetSoftnetAllow) > 0 {
		spec.NetSoftnetAllow = &params.NetSoftnetAllow
	}

	if len(params.NetSoftnetBlock) > 0 {
		spec.NetSoftnetBlock = &params.NetSoftnetBlock
	}

	if len(params.HostDirs) > 0 {
		hostDirs := make([]struct {
			Name *string `json:"name,omitempty"`
			Path *string `json:"path,omitempty"`
			Ro   *bool   `json:"ro,omitempty"`
		}, len(params.HostDirs))
		for i, hd := range params.HostDirs {
			hostDirs[i] = struct {
				Name *string `json:"name,omitempty"`
				Path *string `json:"path,omitempty"`
				Ro   *bool   `json:"ro,omitempty"`
			}{
				Name: &hd.Name,
				Path: &hd.Path,
				Ro:   hd.Ro,
			}
		}
		spec.HostDirs = &hostDirs
	}

	if len(params.Labels) > 0 {
		spec.Labels = &params.Labels
	}

	if len(params.Resources) > 0 {
		spec.Resources = &params.Resources
	}

	if params.ImagePullPolicy != nil {
		policy := orchardclient.VMSpecImagePullPolicy(*params.ImagePullPolicy)
		spec.ImagePullPolicy = &policy
	}

	if params.RestartPolicy != nil {
		policy := orchardclient.VMSpecRestartPolicy(*params.RestartPolicy)
		spec.RestartPolicy = &policy
	}

	return spec
}

func isVMUpToDate(params *v1alpha1.VMParameters, vm *orchardclient.VM) bool {
	// Compare key fields to determine if update is needed
	// This is a simplified comparison - you may want to add more fields

	if vm.Image != nil && *vm.Image != params.Image {
		return false
	}

	if params.CPU != nil && vm.Cpu != nil && *vm.Cpu != float32(*params.CPU) {
		return false
	}

	if params.Memory != nil && vm.Memory != nil && *vm.Memory != float32(*params.Memory) {
		return false
	}

	// Add more comparisons as needed

	return true
}

func stringValue(s *orchardclient.VMStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

// getCloudInitStatus reads the cloud-init status from CR status
func getCloudInitStatus(cr *v1alpha1.VM) string {
	return cr.Status.AtProvider.CloudInitStatus
}

// setCloudInitStatus updates the cloud-init status in CR status
func setCloudInitStatus(cr *v1alpha1.VM, status, message string) {
	cr.Status.AtProvider.CloudInitStatus = status
	cr.Status.AtProvider.CloudInitMessage = message
}

// truncateMessage ensures the message fits within the condition message limit
func truncateMessage(msg string) string {
	if len(msg) <= maxConditionMessageLen {
		return msg
	}
	return msg[:maxConditionMessageLen-3] + "..."
}

// getSSHCredentials returns SSH username and password from spec or defaults
func getSSHCredentials(params *v1alpha1.VMParameters) (string, string) {
	username := DefaultSSHUsername
	password := DefaultSSHPassword

	if params.Username != nil && *params.Username != "" {
		username = *params.Username
	}
	if params.Password != nil && *params.Password != "" {
		password = *params.Password
	}

	return username, password
}

// buildTunnelConfig creates SSH tunnel configuration from CR and client
func (c *external) buildTunnelConfig(cr *v1alpha1.VM) ssh.TunnelConfig {
	username, password := getSSHCredentials(&cr.Spec.ForProvider)

	return ssh.TunnelConfig{
		OrchardBaseURL: c.baseURL,
		BearerToken:    c.token,
		VMName:         meta.GetExternalName(cr),
		SSHUsername:    username,
		SSHPassword:    password,
		SSHPort:        22,
		WaitSeconds:    30,
		Timeout:        60 * time.Second,
	}
}

// isSSHReady tests if SSH connection is available by running a simple command
func (c *external) isSSHReady(ctx context.Context, cr *v1alpha1.VM) bool {
	config := c.buildTunnelConfig(cr)

	session, err := ssh.NewVMSession(ctx, config)
	if err != nil {
		return false
	}
	defer session.Close()

	// Run a simple command to verify SSH works
	result, err := session.ExecuteCommand(ctx, "ls -al")
	return err == nil && result.ExitCode == 0
}

// handleCloudInit checks if cloud-init is needed and handles execution
func (c *external) handleCloudInit(ctx context.Context, cr *v1alpha1.VM) error {
	// Check if there's a startup script to execute
	if cr.Spec.ForProvider.StartupScript == nil || cr.Spec.ForProvider.StartupScript.ScriptContent == "" {
		// No startup script - VM is available immediately
		cr.SetConditions(xpv1.Available())
		return nil
	}

	// Check cloud-init status annotation
	status := getCloudInitStatus(cr)

	switch status {
	case CloudInitStatusCompleted:
		cr.SetConditions(xpv1.Available())
		return nil
	case CloudInitStatusFailed:
		cond := xpv1.Unavailable()
		cond.Message = truncateMessage(cr.Status.AtProvider.CloudInitMessage)
		cr.SetConditions(cond)
		return nil
	case CloudInitStatusRunning:
		cr.SetConditions(xpv1.Creating())
		return nil
	default:
		// Empty or "pending" - execute cloud-init
		return c.executeCloudInit(ctx, cr)
	}
}

// executeCloudInit runs the startup script via SSH
func (c *external) executeCloudInit(ctx context.Context, cr *v1alpha1.VM) error {
	// Set status to running
	setCloudInitStatus(cr, CloudInitStatusRunning, "")

	// Check SSH readiness first
	if !c.isSSHReady(ctx, cr) {
		// SSH not ready yet - return error to retry later
		setCloudInitStatus(cr, CloudInitStatusPending, "waiting for SSH")
		return errors.New(errSSHNotReady)
	}

	config := c.buildTunnelConfig(cr)
	script := cr.Spec.ForProvider.StartupScript.ScriptContent
	env := cr.Spec.ForProvider.StartupScript.Env

	// Execute the script via SSH
	result, err := ssh.UploadAndRunScript(ctx, config, script, env)
	if err != nil {
		message := fmt.Sprintf("cloud-init failed: %s", err.Error())
		setCloudInitStatus(cr, CloudInitStatusFailed, message)
		cond := xpv1.Unavailable()
		cond.Message = truncateMessage(message)
		cr.SetConditions(cond)
		return nil // Don't return error - we've handled it by setting status
	}

	if result.ExitCode != 0 {
		message := fmt.Sprintf("exit code %d: %s", result.ExitCode, result.Stderr)
		setCloudInitStatus(cr, CloudInitStatusFailed, message)
		cond := xpv1.Unavailable()
		cond.Message = truncateMessage(message)
		cr.SetConditions(cond)
		return nil // Don't return error - we've handled it by setting status
	}

	// Success
	setCloudInitStatus(cr, CloudInitStatusCompleted, "")
	cr.SetConditions(xpv1.Available())
	return nil
}
