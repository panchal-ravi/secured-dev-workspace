// vectorAdd.cu — minimal CUDA sanity check for the GPU workspace.
//
// Adds two vectors on the GPU and verifies the result on the host. A non-zero
// exit (or "Test FAILED") means the CUDA toolchain/driver/GPU aren't wired up.
// Build: `make`  Run: `./vectorAdd`  (see README.md)

#include <cstdio>
#include <cstdlib>
#include <cmath>
#include <cuda_runtime.h>

// One element per thread: C[i] = A[i] + B[i].
__global__ void vectorAdd(const float *A, const float *B, float *C, int n) {
    int i = blockDim.x * blockIdx.x + threadIdx.x;
    if (i < n) {
        C[i] = A[i] + B[i];
    }
}

// Abort with a clear message if a CUDA call fails.
static void check(cudaError_t err, const char *what) {
    if (err != cudaSuccess) {
        fprintf(stderr, "CUDA error during %s: %s\n", what, cudaGetErrorString(err));
        exit(EXIT_FAILURE);
    }
}

int main(void) {
    const int n = 50000;
    const size_t bytes = n * sizeof(float);

    // Report the device so the run doubles as a visibility check.
    int devCount = 0;
    check(cudaGetDeviceCount(&devCount), "cudaGetDeviceCount");
    if (devCount == 0) {
        fprintf(stderr, "No CUDA devices found.\n");
        return EXIT_FAILURE;
    }
    cudaDeviceProp prop;
    check(cudaGetDeviceProperties(&prop, 0), "cudaGetDeviceProperties");
    printf("CUDA device 0: %s (compute %d.%d), %d device(s) visible\n",
           prop.name, prop.major, prop.minor, devCount);

    // Host buffers.
    float *hA = (float *)malloc(bytes);
    float *hB = (float *)malloc(bytes);
    float *hC = (float *)malloc(bytes);
    if (!hA || !hB || !hC) {
        fprintf(stderr, "Host allocation failed.\n");
        return EXIT_FAILURE;
    }
    for (int i = 0; i < n; ++i) {
        hA[i] = (float)i;
        hB[i] = (float)(2 * i);
    }

    // Device buffers.
    float *dA = nullptr, *dB = nullptr, *dC = nullptr;
    check(cudaMalloc((void **)&dA, bytes), "cudaMalloc dA");
    check(cudaMalloc((void **)&dB, bytes), "cudaMalloc dB");
    check(cudaMalloc((void **)&dC, bytes), "cudaMalloc dC");

    check(cudaMemcpy(dA, hA, bytes, cudaMemcpyHostToDevice), "cudaMemcpy A H2D");
    check(cudaMemcpy(dB, hB, bytes, cudaMemcpyHostToDevice), "cudaMemcpy B H2D");

    const int threadsPerBlock = 256;
    const int blocks = (n + threadsPerBlock - 1) / threadsPerBlock;
    vectorAdd<<<blocks, threadsPerBlock>>>(dA, dB, dC, n);
    check(cudaGetLastError(), "kernel launch");
    check(cudaDeviceSynchronize(), "cudaDeviceSynchronize");

    check(cudaMemcpy(hC, dC, bytes, cudaMemcpyDeviceToHost), "cudaMemcpy C D2H");

    // Verify on the host.
    for (int i = 0; i < n; ++i) {
        if (fabs(hC[i] - (hA[i] + hB[i])) > 1e-5) {
            fprintf(stderr, "Test FAILED at element %d (%f != %f)\n", i, hC[i], hA[i] + hB[i]);
            return EXIT_FAILURE;
        }
    }
    printf("Test PASSED\n");

    cudaFree(dA);
    cudaFree(dB);
    cudaFree(dC);
    free(hA);
    free(hB);
    free(hC);
    check(cudaDeviceReset(), "cudaDeviceReset");
    return EXIT_SUCCESS;
}
