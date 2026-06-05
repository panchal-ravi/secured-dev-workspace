# Build the CUDA vectorAdd sample. The gpu-workspace image ships nvcc (CUDA devel
# base). -arch=sm_75 targets the NVIDIA T4 (g4dn). Override NVCC/ARCH if needed.
NVCC ?= nvcc
ARCH ?= sm_75

vectorAdd: vectorAdd.cu
	$(NVCC) -arch=$(ARCH) -o vectorAdd vectorAdd.cu

.PHONY: run clean
run: vectorAdd
	./vectorAdd

clean:
	rm -f vectorAdd
