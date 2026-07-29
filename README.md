# tekton-kueue

Controller for integrating [Tekton] with [Kueue].

## Description

The controller enables [Kueue] to manage the scheduling of [Tekton] PipelineRuns.

## Getting Started

### Prerequisites
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.
- make
- Get familiar with [basic Kueue concept](https://kueue.sigs.k8s.io/docs/concepts/)

### To Deploy on the cluster

**Install CertManager:**

CertManager is required for providing certificates for the admission webhook.

```sh
make cert-manager
```

**Install Kueue:**

The controller currently supports kueue v0.10.x
If you already have kueue installed, make sure to enable it by adding `pipelineruns.tekton.dev` to the external frameworks.
Otherwise you can install it with:

```sh
make kueue
```

**Install Tekton:**
```sh
make tekton
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=quay.io/konflux-ci/tekton-kueue:latest
```

### To Uninstall

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

### Usage

The controller has an admission webhook that will associate any PipelineRun created
on the cluster with a `LocalQueue` named `pipelines-queue`.
The association is made using a label.

You can limit the admission webhook to act on [certain namespaces](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#matching-requests-namespaceselector) by modifying config/webhook/manifests.yaml

In order to use [Kueue], you need to create (at least) the following resource:

- [ResourceFlavor]
- [ClusterQueue]
- [LocalQueue]

For convenience, we will provide a sample of those resources.

**Note:** Some of the resources are namespaced, so make sure to create a namespace for testing before applying the samples.

Create a namespace:

```sh
kubectl create namespace tekton-kueue-test
```

You can apply the samples using:

```sh
kubectl apply -n tekton-kueue-test -f config/samples/kueue/kueue-resources.yaml
```

Resource requests can be specified by placing annotations on the PipelineRun resource. The available annotations are:

- `kueue.konflux-ci.dev/requests-cpu`
- `kueue.konflux-ci.dev/requests-memory`
- `kueue.konflux-ci.dev/requests-storage`
- `kueue.konflux-ci.dev/requests-ephemeral-storage`

Additionally, dynamic resource requests can be created using CEL expressions with the `resource()` function, which automatically creates prefixed annotations (e.g., `kueue.konflux-ci.dev/requests-aws-vm-x`).

By default, a special resource called `tekton.dev/pipelineruns` is added to the [Workload] with the value of 1.
This resource can be used for controlling the number of PipelineRuns that can be executed concurrently.

You are now ready to create PipelineRuns that will get scheduled by Kueue.
You can use the following PipelineRun definition (which prints a message to stdout after sleeping for several seconds):

```sh
kubectl create -n tekton-kueue-test -f config/samples/pipelines/pipeline.yaml
```

After creating the PipelineRun, You can see the status of the [ClusterQueue] by running

```sh
kubectl get -o yaml clusterqueue cluster-pipeline-queue
```

You can also see the [Workload] resource that the `tekton-kueue` controller created

```sh
kubectl get -n tekton-kueue-test workloads
```

If You'll try to create several PipelineRuns at one, you would see that some
of them get queued because the [ClusterQueue] resource reaches its resource limit.

### Usage with MultiKueue

In a [MultiKueue] setup, `tekton-kueue` should be deployed on the manager/hub cluster with MultiKueue Override set.

When running in this mode, the admission webhook will automatically set the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` for all `PipelineRuns`. This field signals to the `tekton-kueue` controller that the `PipelineRun` is being managed by `MultiKueue`. This allows the `MultiKueue` controller to manage the `PipelineRun`'s lifecycle.

To enable this mode, you must create a `ConfigMap` with the `multiKueueOverride` field set to `true`.

Create a `ConfigMap` named `tekton-kueue-config` in the same namespace as the controller:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: tekton-kueue-config
  namespace: tekton-kueue-system
data:
  config.yaml: |
    multiKueueOverride: true
```
The controller will automatically load the configuration from this `ConfigMap`.

#### CEL-Based Routing (Alternative)

For conditional per-PipelineRun routing, use the CEL `multiKueue()` function instead of the boolean `multiKueueOverride`. This lets you route only specific PipelineRuns to MultiKueue based on labels, namespace, or other properties:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: tekton-kueue-config
  namespace: tekton-kueue-system
data:
  config.yaml: |
    queueName: default-queue
    cel:
      expressions:
        # Route build pipelines to spoke cluster via MultiKueue,
        # keep everything else on the hub
        - |
          has(pipelineRun.metadata.labels) &&
          "pipeline-type" in pipelineRun.metadata.labels &&
          pipelineRun.metadata.labels["pipeline-type"] == "build" ?
            [multiKueue("multikueue-queue")] : []
```

> **Note:** When using CEL routing functions (`managedBy()`, `multiKueue()`), do not set `multiKueueOverride: true`. The boolean sets `Spec.ManagedBy` for all PipelineRuns unconditionally *before* CEL expressions are evaluated, which prevents conditional per-PipelineRun routing. Use `multiKueueOverride` only when *all* PipelineRuns should be routed to MultiKueue.

## Command Line Interface

The `tekton-kueue` binary provides several subcommands:

### `mutate` - Apply PipelineRun Mutations

The `mutate` subcommand allows you to test and preview how PipelineRuns will be modified by the webhook before deploying them to the cluster. This is useful for:

- Testing CEL expressions and mutation logic
- Previewing changes before applying them
- Debugging configuration issues
- Validating PipelineRun transformations

#### Usage

```sh
tekton-kueue mutate --pipelinerun-file <path> --config-dir <path>
```

#### Parameters

- `--pipelinerun-file`: Path to the file containing the PipelineRun definition (required)
- `--config-dir`: Path to the directory containing the configuration file (required)
- `--zap-log-level`: Set logging level (debug, info, error)

#### Example

Create a test PipelineRun file:

```yaml
# test-pipelinerun.yaml
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: test-pipeline-run
  namespace: default
spec:
  pipelineRef:
    name: test-pipeline
  workspaces:
    - name: shared-workspace
      emptyDir: {}
```

Create a configuration file:

```yaml
# config/config.yaml
queueName: "test-queue"
cel:
  expressions:
    - 'annotation("tekton.dev/mutated-by", "tekton-kueue")'
    - 'annotation("tekton.dev/namespace", plrNamespace)'
    - 'annotation("tekton.dev/event-type", pacEventType)'
    - 'annotation("tekton.dev/test-event-type", pacTestEventType)'
    - 'label("environment", "test")'
    - 'priority("medium")'
    - 'resource("aws-vm-x", 2)'
    - '[annotation("build.tekton.dev/timestamp", "2025-01-01T00:00:00Z"), label("app", "test-app")]'
```

Run the mutation:

```sh
tekton-kueue mutate --pipelinerun-file test-pipelinerun.yaml --config-dir config/
```

This will output the mutated PipelineRun with:
- Applied annotations from CEL expressions (including namespace, event type, and test event type)
- Applied labels from CEL expressions  
- Priority class label (`kueue.x-k8s.io/priority-class`)
- Resource request annotations (e.g., `kueue.konflux-ci.dev/requests-aws-vm-x`)
- Queue name label (`kueue.x-k8s.io/queue-name`)
- Status set to `PipelineRunPending`

#### CEL Expression Examples

The configuration supports [CEL (Common Expression Language)](https://github.com/google/cel-spec) expressions for dynamic mutations.

##### Available Variables

The following variables are available in CEL expressions:

- `pipelineRun`: The complete PipelineRun object as a map
- `plrNamespace`: The namespace of the PipelineRun (shorthand for `pipelineRun.metadata.namespace`)
- `pacEventType`: The Pipelines as Code event type (from `pipelinesascode.tekton.dev/event-type` label, empty string if not present)
- `pacTestEventType`: The Integration test event type (from `pac.test.appstudio.openshift.io/event-type` label, empty string if not present)

**Benefits of convenience variables:**
- **Shorter syntax**: Use `plrNamespace` instead of `pipelineRun.metadata.namespace`
- **Null safety**: `pacEventType` and `pacTestEventType` handle missing labels gracefully (return empty string)
- **Better readability**: Complex expressions become more concise and readable

##### Expression Examples

```yaml
cel:
  expressions:
    # Single annotation
    - 'annotation("tekton.dev/pipeline", pipelineRun.metadata.name)'
    
    # Single label
    - 'label("environment", "production")'
    
    # Multiple mutations in one expression
    - '[annotation("build.time", "2025-01-01T00:00:00Z"), label("team", "platform")]'
    
    # Priority function - sets Kueue priority class label
    - 'priority("high")'
    - 'priority(pipelineRun.metadata.namespace == "production" ? "high" : "low")'
    
    # Using convenience variables
    - 'annotation("namespace", plrNamespace)'
    - 'annotation("event-type", pacEventType)'
    - 'annotation("test-event-type", pacTestEventType)'
    - 'priority(plrNamespace == "production" ? "high" : "low")'
    
    # Conditional mutations based on PipelineRun data
    - 'pipelineRun.metadata.namespace == "prod" ? label("priority", "high") : label("priority", "normal")'
    - 'plrNamespace == "production" ? label("environment", "prod") : label("environment", "dev")'
    - 'pacEventType == "push" ? annotation("trigger", "push-event") : annotation("trigger", "other-event")'
    - 'pacTestEventType != "" ? label("test-type", pacTestEventType) : label("test-type", "none")'
    
    # managedBy - set Spec.ManagedBy to a custom controller (standalone example)
    - 'managedBy("tekton.dev/pipeline")'
    
    # multiKueue - route to spoke cluster via MultiKueue (standalone example)
    - 'multiKueue()'
    
    # multiKueue with queue - route to spoke AND override queue name (standalone example)
    - 'multiKueue("multikueue-queue")'
```

> **Note:** `managedBy()`, `multiKueue()`, and `multiKueue("queueName")` all set `Spec.ManagedBy`. Using more than one unconditionally in the same config does not make sense — the last one silently overrides the others. Use them in conditional expressions (ternaries) for per-PipelineRun routing, or pick a single routing strategy.

```yaml
cel:
  expressions:
    # Conditional routing - builds to spoke, releases stay on hub
    - 'has(pipelineRun.metadata.labels) && "pipeline-type" in pipelineRun.metadata.labels && pipelineRun.metadata.labels["pipeline-type"] == "build" ? [multiKueue("multikueue-queue")] : []'
    - 'has(pipelineRun.metadata.labels) && "pipeline-type" in pipelineRun.metadata.labels && pipelineRun.metadata.labels["pipeline-type"] == "release" ? [managedBy("tekton.dev/pipeline")] : []'
    
    # Multiline CEL expression for multiple mutations
    # This expression applies several annotations and labels in one go
    - |
      [
        annotation("tekton.dev/pipeline", pipelineRun.metadata.name),
        annotation("tekton.dev/namespace", plrNamespace),
        annotation("tekton.dev/event-type", pacEventType),
        annotation("tekton.dev/test-event-type", pacTestEventType),
        label("app", "tekton-pipeline"),
        label("version", "v1"),
        priority(plrNamespace == "production" ? "high" : "low"),
        plrNamespace == "production" ? 
          label("environment", "prod") : 
          label("environment", "dev")
      ]
```

**What the multiline expression does:**

1. **Creates annotations** from PipelineRun metadata (pipeline name, namespace, event type, and test event type)
2. **Adds standard labels** that apply to all PipelineRuns (`app` and `version`)
3. **Sets priority class** based on namespace using the convenience variable `plrNamespace`
4. **Applies conditional logic** to set the `environment` label based on the namespace
5. **Uses YAML multiline syntax** (`|`) to make complex expressions readable
6. **Returns a list** of mutations that are all applied together

For a PipelineRun named `my-pipeline` in namespace `production` with event type `push` and test event type `unit-test`, this would add:
- Annotations: `tekton.dev/pipeline: my-pipeline`, `tekton.dev/namespace: production`, `tekton.dev/event-type: push`, `tekton.dev/test-event-type: unit-test`
- Labels: `app: tekton-pipeline`, `version: v1`, `environment: prod`, `kueue.x-k8s.io/priority-class: high`

##### Priority Function

The `priority()` function is a specialized CEL function that sets the Kueue priority class label:

- **Function**: `priority(value)`
- **Purpose**: Sets the `kueue.x-k8s.io/priority-class` label on PipelineRuns
- **Usage**: Integrates with Kueue's [workload prioritization](https://kueue.sigs.k8s.io/docs/concepts/workload/#priority) feature

Examples:
```yaml
cel:
  expressions:
    # Static priority
    - 'priority("high")'
    
    # Dynamic priority based on namespace
    - 'priority(pipelineRun.metadata.namespace == "production" ? "high" : "low")'
    
    # Combined with other mutations
    - '[priority("medium"), annotation("queue", "default")]'
```

The priority function automatically:
- Creates a **label** with key `kueue.x-k8s.io/priority-class`
- Sets the label value to the provided string
- Can be used with dynamic expressions referencing PipelineRun fields
- Integrates with Kueue's priority-based scheduling system

##### Resource Function

The `resource()` function is a specialized CEL function that creates resource request annotations with special summing behavior:

- **Function**: `resource(key, value)`
- **Parameters**: 
  - `key`: String representing the resource name (e.g., `"aws-vm-x"`)
  - `value`: Positive integer representing the resource quantity (must be >= 0)
- **Purpose**: Creates annotations for resource requests with automatic key prefixing and value summing for duplicates
- **Usage**: Enables dynamic resource allocation based on PipelineRun properties

**Key Features:**

1. **Automatic Key Prefixing**: Resource keys are automatically prefixed with `kueue.konflux-ci.dev/requests-`
2. **Value Summing**: Multiple resource requests with the same key are automatically summed together
3. **Positive Values Only**: Only non-negative integers are accepted as resource values
4. **Type Safety**: Enforces string keys and integer values at compile time

Examples:
```yaml
cel:
  expressions:
    # Single resource request
    - 'resource("aws-vm-x", 2)'
    
    # Multiple resources with automatic summing
    - 'resource("aws-vm-y", 1000)'
    - 'resource("aws-vm-y", 500)'  # Results in total: 1500
    
    # Dynamic resource allocation based on PipelineRun
    - 'resource("ibm-vm-z", pipelineRun.metadata.namespace == "production" ? 4 : 2)'
    
    # Combined with other mutations
    - '[resource("aws-vm-x", 3), annotation("queue", "high-priority"), label("team", "platform")]'
    
    # Conditional resource allocation
    - 'plrNamespace == "production" ? resource("aws-vm-y", 8) : resource("aws-vm-y", 4)'
```

**What happens with resource requests:**

For `resource("aws-vm-x", 2)`, the function:
1. **Validates** the key and value (key must be non-empty, value must be >= 0)
2. **Prefixes** the key: `"aws-vm-x"` becomes `"kueue.konflux-ci.dev/requests-aws-vm-x"`
3. **Creates** an annotation with the prefixed key and string value `"2"`
4. **Sums** with existing values if the same resource key appears multiple times

**Error Handling:**

The resource function performs validation and will fail with clear error messages for:
- Empty resource keys: `resource key cannot be empty`
- Negative values: `resource value must be positive (>= 0), got -100`
- Invalid key formats: Keys must follow Kubernetes annotation naming rules

### Other Subcommands

- `controller` - Run the tekton-kueue controller
- `webhook` - Run the admission webhook server

## Metrics

Both controller and webhook server expose the built-in metrics provided by controller-runtime.
In addition, the tekton-kueue webhook server exposes custom Prometheus metrics for monitoring and observability:

### Available Metrics

| Metric Name | Type | Description | Labels |
|-------------|------|-------------|--------|
| `tekton_kueue_cel_evaluations_total` | Counter | Total number of CEL evaluations in the webhook | `result` (success, failure) |
| `tekton_kueue_cel_mutations_total` | Counter | Total number of CEL mutation operations applied to PipelineRuns | `result` (success, failure) |

### Metrics Details

#### `tekton_kueue_cel_evaluations_total`

- **Type**: Counter
- **Purpose**: Tracks the total number of CEL expression evaluations during PipelineRun mutation processing
- **Labels**: 
  - `result`: The outcome of the CEL evaluation
    - `success`: CEL expression evaluated successfully
    - `failure`: CEL expression failed to evaluate
- **When incremented**: 
  - Every time CEL expressions are evaluated during webhook processing
  - Increments with `result="success"` for successful evaluations
  - Increments with `result="failure"` for failed evaluations
- **Use cases**: 
  - Monitor the overall health and usage of CEL expressions in your configuration
  - Calculate error rates: `rate(tekton_kueue_cel_evaluations_total{result="failure"}[5m]) / rate(tekton_kueue_cel_evaluations_total[5m])`
  - Alert on unexpected increases in evaluation failures
  - Track CEL expression usage patterns and performance

#### `tekton_kueue_cel_mutations_total`

- **Type**: Counter
- **Purpose**: Tracks the total number of CEL mutation operations applied to PipelineRuns
- **Labels**: 
  - `result`: The outcome of the mutation operation
    - `success`: All mutations applied successfully to the PipelineRun
    - `failure`: One or more mutations failed to apply (e.g., validation errors, parsing errors)
- **When incremented**: 
  - Increments with `result="success"` when all mutations from CEL expressions are successfully applied to a PipelineRun
  - Increments with `result="failure"` when any mutation fails during application (e.g., invalid resource values, annotation/label validation errors)
- **Use cases**: 
  - Monitor the success rate of PipelineRun mutations in the webhook
  - Calculate mutation failure rates: `rate(tekton_kueue_cel_mutations_total{result="failure"}[5m]) / rate(tekton_kueue_cel_mutations_total[5m])`
  - Alert on unexpected increases in mutation application failures
  - Track the overall health of the mutation pipeline and identify configuration issues

## Project Distribution

The project is built by [Konflux]. Images are published to [quay.io/konflux-ci/tekton-queue](quay.io/konflux-ci/tekton-queue)

## Contributing

**NOTE:** Run `make help` for more information on all potential `make` targets.

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)


[Tekton]: <https://tekton.dev/> "Tekton"
[Kueue]: <https://kueue.sigs.k8s.io/> "Kueue"
[Konflux]: <https://konflux-ci.dev/> "Konflux"
[ResourceFlavor]: <https://kueue.sigs.k8s.io/docs/concepts/resource_flavor/> "ResourceFlavor"
[ClusterQueue]: <https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/> "ClusterQueue"
[LocalQueue]: <https://kueue.sigs.k8s.io/docs/concepts/local_queue/> "LocalQueue"
[Workload]: <https://kueue.sigs.k8s.io/docs/concepts/workload/> "Workload"
