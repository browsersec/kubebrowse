# Local Development with Tilt

This document describes how to use Tilt for local development of the Kubebrowse project on Kubernetes. Tilt provides a streamlined workflow with live updates, automated builds, and easy resource management.

## Prerequisites

1.  **Docker and Encore**: Ensure Docker is installed and running. Install and authenticate the [Encore CLI](https://encore.dev/docs/install); Tilt invokes `encore build docker` to build the API image.
2.  **Kubernetes Cluster**: You need a local Kubernetes cluster. Options include:
    *   [Kind (Kubernetes IN Docker)](https://kind.sigs.k8s.io/docs/user/quick-start/)
    *   [Minikube](https://minikube.sigs.k8s.io/docs/start/)
    *   Docker Desktop's built-in Kubernetes cluster.
    Ensure your `kubectl` context is pointing to your local development cluster (`kubectl config current-context`).
3.  **Tilt**: Install Tilt by following the instructions on the official Tilt website: [Install Tilt](https://docs.tilt.dev/install.html)

## Getting Started

1.  **Clone the Repository**:
    If you haven't already, clone the project repository:
    ```bash
    git clone https://github.com/browsersec/KubeBrowse
    cd KubeBrowse
    ```

2.  **Configure Kubernetes Context (if needed)**:
    The `Tiltfile` attempts to use your current Kubernetes context. If Tilt has trouble connecting or you need to specify a context:
    *   Uncomment and modify the `allow_k8s_contexts` line in the `Tiltfile` at the root of the project. For example:
        ```python
        # allow_k8s_contexts('kind-kind', 'docker-desktop')
        ```
    *   Replace `'kind-kind'` or `'docker-desktop'` with the name of your `kubectl` context.

3.  **Start Tilt**:
    Navigate to the root directory of the project in your terminal and run:
    ```bash
    tilt up
    ```
    Tilt will:
    *   Build the Encore API image and deploy the Kubernetes resources in `deployments/manifest.yml`.
    *   Deploy all resources defined in `deployments/manifest.yml` to your Kubernetes cluster.
    *   Set up port forwarding for accessing the services locally:
        *   Backend API (`browser-sandbox-api`): `localhost:8080`
    *   Open a web browser with the Tilt UI, where you can monitor the status of your services, view logs, and manage resources.

4.  **Accessing the Application**:
    *   API: Accessible at `http://localhost:8080`

## Development Workflow

*   **API rebuilds**: Changes under `api/` and `internal/` trigger the `encore-build` resource, which rebuilds the self-hosted image. Restart the API resource after a successful image build.

*   **Logs**: View real-time logs for each service in the Tilt UI.

*   **Resource Management**: The Tilt UI allows you to manually trigger updates, restart pods, and view Kubernetes resource details.

## Stopping Tilt

*   To stop Tilt and remove the deployed resources from your Kubernetes cluster, press `Ctrl+C` in the terminal where `tilt up` is running.
*   Alternatively, you can use `tilt down`.

## Troubleshooting

*   **`tilt doctor`**: Run `tilt doctor` in your terminal. This command checks your Tilt and Kubernetes setup and provides suggestions for fixing common issues.
*   **Kubernetes Context**: Ensure `kubectl config current-context` points to the correct local cluster. If not, use `kubectl config use-context <your-local-cluster-context>`.
*   **Image Pull Issues**: If Kubernetes has trouble pulling images, ensure your `imagePullPolicy` in `deployments/manifest.yml` is `IfNotPresent` for the services managed by Tilt (api, frontend). This should already be set by a previous step. Also, check your local Docker environment and Kubernetes cluster's ability to access local images (e.g., if using Minikube, you might need `minikube image load <image_name>` or configure Docker environment sharing). The `update_mode(UPDATE_MODE_AUTO)` in Tiltfile usually handles this for local clusters.
*   **Port Conflicts**: If port 8080 is already in use, change the API `port_forwards` entry in the `Tiltfile`.

This setup should significantly improve the local development experience when working with Kubernetes.
