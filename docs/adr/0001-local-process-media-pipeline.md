# Use local processes for media and AI analysis

The application will orchestrate FFmpeg and FFprobe, whisper.cpp, and aubio as local executables, and call an LM Studio vision model through its loopback HTTP API. This keeps the Go application free of cgo and model-runtime bindings, preserves local-only processing, and allows each specialist tool to be upgraded independently at the cost of explicit dependency diagnostics and process management.
