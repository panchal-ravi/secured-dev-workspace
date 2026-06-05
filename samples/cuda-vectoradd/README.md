# CUDA vectorAdd — GPU workspace smoke test

A minimal CUDA program that adds two vectors on the GPU and verifies the result on
the host. Use it to confirm a `gpu-workspace` is wired up end to end (driver →
container runtime → CUDA toolchain → GPU).

## Run it

```bash
# 1. Confirm the GPU is visible inside the workspace
nvidia-smi

# 2. Build with the bundled CUDA toolchain (nvcc ships in the gpu-workspace image)
make

# 3. Run — expect "Test PASSED"
./vectorAdd
```

Expected output (NVIDIA T4 on g4dn):

```
CUDA device 0: Tesla T4 (compute 7.5), 1 device(s) visible
Test PASSED
```

If `nvidia-smi` fails or the device count is 0, the GPU isn't exposed to the
container — check the Nomad `nvidia/gpu` device request and the `nvidia` docker
runtime on the workspace job, and the `nomad-device-nvidia` plugin on the node.

`make clean` removes the built binary.
