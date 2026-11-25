// Package mediaprobe runs the embedded MediaInfoLib WASI module to extract
// media facts without any external `mediainfo` executable.
//
// The module under mediainfo.wasm.gz is a wasm32-wasi reactor built from
// pinned upstream sources (see scripts/build/mediainfo-wasm/manifest.json) and
// executed with the pure-Go wazero runtime, so the probe works identically on
// every supported GOOS/GOARCH with CGO disabled and an empty PATH.
//
// Files are streamed through MediaInfo's buffer API in fixed-size chunks;
// MediaInfo asks for absolute offsets via Open_Buffer_Continue_GoTo_Get, which
// this package turns into seeks, so probes never load whole media files into
// memory. Each probe runs in its own module instance, which keeps concurrent
// probes independent.
package mediaprobe

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed mediainfo.wasm.gz
var moduleGzip []byte

const (
	// streamChunkSize bounds how many bytes of the media file are handed to
	// MediaInfo per buffer iteration. MediaInfo mostly parses container
	// headers, so a moderate chunk keeps throughput high and the per-instance
	// linear-memory footprint low.
	streamChunkSize = 1 << 20

	// statusFinalized is MediaInfoLib's Open_Buffer_Continue bit 3: no more
	// data is needed and further bytes will not be used.
	statusFinalized = 0x08

	// noGoTo is Open_Buffer_Continue_GoTo_Get's "keep reading sequentially".
	noGoTo = ^uint64(0)

	// DefaultMaxOutputBytes caps the JSON report size when callers do not
	// have their own limit. Real reports are tens of kilobytes.
	DefaultMaxOutputBytes = 2 << 20
)

// Sentinel errors classifying probe failures for callers. Use errors.Is.
var (
	// ErrUnavailable means the embedded engine itself could not be used
	// (corrupt artifact, unsupported host, allocation failure).
	ErrUnavailable = errors.New("mediaprobe: embedded MediaInfo engine unavailable")
	// ErrProbeFailed means MediaInfo rejected the input or the probe loop
	// broke down (unreadable file, protocol violation).
	ErrProbeFailed = errors.New("mediaprobe: MediaInfo probe failed")
	// ErrNotRegularFile means the target is not a regular file.
	ErrNotRegularFile = errors.New("mediaprobe: not a regular file")
	// ErrOutputLimit means the MediaInfo report exceeded the caller's cap.
	ErrOutputLimit = errors.New("mediaprobe: MediaInfo output exceeded limit")
)

// OutputLimitError reports a report that grew past maxOutputBytes.
type OutputLimitError struct {
	Limit int64
	Got   uint64
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("%v: report is %d bytes, limit is %d", ErrOutputLimit, e.Got, e.Limit)
}

func (e *OutputLimitError) Is(target error) bool { return target == ErrOutputLimit }

// engine is the process-wide compiled module. Compilation happens once; every
// probe instantiates its own copy of the module state.
var engine = struct {
	sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	err      error
}{}

func loadEngine() error {
	engine.Do(func() {
		compressed, err := gzip.NewReader(bytes.NewReader(moduleGzip))
		if err != nil {
			engine.err = fmt.Errorf("%w: artifact is not gzip: %v", ErrUnavailable, err)
			return
		}
		wasm, err := io.ReadAll(compressed)
		closeErr := compressed.Close()
		if err != nil || closeErr != nil {
			engine.err = fmt.Errorf("%w: artifact decompress failed", ErrUnavailable)
			return
		}
		// Compilation is process-wide state: it must not be tied to the
		// first caller's context, which may be cancelled while later probes
		// still need the module.
		ctx := context.Background()
		runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
		compiled, err := runtime.CompileModule(ctx, wasm)
		if err != nil {
			_ = runtime.Close(ctx)
			engine.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
			_ = runtime.Close(ctx)
			engine.err = fmt.Errorf("%w: wasi host functions: %v", ErrUnavailable, err)
			return
		}
		engine.runtime = runtime
		engine.compiled = compiled
	})
	return engine.err
}

// probeModule is one live module instance plus its resolved exports.
type probeModule struct {
	module api.Module

	newHandle      api.Function
	deleteHandle   api.Function
	setOutputJSON  api.Function
	openBufferInit api.Function
	bufferContinue api.Function
	bufferGoToGet  api.Function
	bufferFinalize api.Function
	inform         api.Function
	infoVersion    api.Function
	lastPtr        api.Function
	alloc          api.Function
	free           api.Function
}

// instantiate creates a fresh module instance, runs its reactor initializer
// and resolves the bridge exports. The instance closes when ctx completes
// (the runtime was built with WithCloseOnContextDone), which is how probe
// cancellation interrupts a running MediaInfo call.
func instantiate(ctx context.Context) (*probeModule, error) {
	if err := loadEngine(); err != nil {
		return nil, err
	}
	module, err := engine.runtime.InstantiateModule(ctx, engine.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: instantiate: %v", ErrUnavailable, err)
	}
	pm := &probeModule{module: module}
	ok := true
	resolve := func(name string, dst *api.Function) {
		fn := module.ExportedFunction(name)
		if fn == nil {
			ok = false
			return
		}
		*dst = fn
	}
	if initialize := module.ExportedFunction("_initialize"); initialize != nil {
		if _, err := initialize.Call(ctx); err != nil {
			module.Close(ctx)
			return nil, fmt.Errorf("%w: _initialize: %v", ErrUnavailable, err)
		}
	} else {
		ok = false
	}
	resolve("mi_new", &pm.newHandle)
	resolve("mi_delete", &pm.deleteHandle)
	resolve("mi_set_output_json", &pm.setOutputJSON)
	resolve("mi_open_buffer_init", &pm.openBufferInit)
	resolve("mi_open_buffer_continue", &pm.bufferContinue)
	resolve("mi_open_buffer_goto_get", &pm.bufferGoToGet)
	resolve("mi_open_buffer_finalize", &pm.bufferFinalize)
	resolve("mi_inform", &pm.inform)
	resolve("mi_info_version", &pm.infoVersion)
	resolve("mi_last_ptr", &pm.lastPtr)
	resolve("mi_alloc", &pm.alloc)
	resolve("mi_free", &pm.free)
	if !ok {
		module.Close(ctx)
		return nil, fmt.Errorf("%w: artifact misses bridge exports", ErrUnavailable)
	}
	return pm, nil
}

// call invokes fn and maps wazero failures. A cancelled context surfaces as
// ctx.Err(), everything else means the module trapped.
func (pm *probeModule) call(ctx context.Context, fn api.Function, params ...uint64) (uint64, error) {
	results, err := fn.Call(ctx, params...)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}
	if len(results) == 0 {
		return 0, nil
	}
	return results[0], nil
}

// lastString reads the bridge's result channel (mi_last_ptr + length).
func (pm *probeModule) lastString(length uint32) (string, error) {
	ptr, err := pm.call(context.Background(), pm.lastPtr)
	if err != nil {
		return "", err
	}
	view, ok := pm.module.Memory().Read(uint32(ptr), length)
	if !ok {
		return "", fmt.Errorf("%w: result outside linear memory", ErrProbeFailed)
	}
	return string(view), nil
}

// SelfCheck verifies the embedded engine initializes and reports a plausible
// MediaInfoLib version. It returns the version string, e.g.
// "MediaInfoLib - v25.10".
func SelfCheck(ctx context.Context) (string, error) {
	pm, err := instantiate(ctx)
	if err != nil {
		return "", err
	}
	defer pm.module.Close(ctx)

	length, err := pm.call(ctx, pm.infoVersion)
	if err != nil {
		return "", err
	}
	version, err := pm.lastString(uint32(length))
	if err != nil {
		return "", err
	}
	if version == "" {
		return "", fmt.Errorf("%w: Info_Version is empty", ErrUnavailable)
	}
	return version, nil
}

// ProbeJSON streams path through the embedded MediaInfo engine and returns
// its report in the same JSON shape `mediainfo --Output=JSON` produces.
//
// maxOutputBytes bounds the report; the cap exists for parity with the
// external-binary probe, which also bounded stdout. Files are read in
// streamChunkSize blocks and MediaInfo's GoTo requests become seeks, so the
// probe's memory use does not scale with file size. Cancelling ctx aborts the
// probe promptly, including while a MediaInfo call is running.
func ProbeJSON(ctx context.Context, path string, maxOutputBytes int64) ([]byte, error) {
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, err)
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}

	pm, err := instantiate(ctx)
	if err != nil {
		return nil, err
	}
	defer pm.module.Close(ctx)

	handle, err := pm.call(ctx, pm.newHandle)
	if err != nil {
		return nil, err
	}
	if handle == 0 {
		return nil, fmt.Errorf("%w: mi_new returned a null handle", ErrUnavailable)
	}
	defer func() { _, _ = pm.deleteHandle.Call(context.Background(), handle) }()

	if _, err := pm.call(ctx, pm.setOutputJSON, handle); err != nil {
		return nil, err
	}
	if status, err := pm.call(ctx, pm.openBufferInit, handle, uint64(stat.Size())); err != nil {
		return nil, err
	} else if status == 0 {
		return nil, fmt.Errorf("%w: Open_Buffer_Init rejected the input", ErrProbeFailed)
	}

	chunkPtr, err := pm.call(ctx, pm.alloc, streamChunkSize)
	if err != nil {
		return nil, err
	}
	if chunkPtr == 0 {
		return nil, fmt.Errorf("%w: module out of memory for stream buffer", ErrUnavailable)
	}
	defer func() { _, _ = pm.free.Call(context.Background(), chunkPtr) }()

	if err := pm.stream(ctx, file, stat.Size(), handle, uint32(chunkPtr)); err != nil {
		return nil, err
	}
	if _, err := pm.call(ctx, pm.bufferFinalize, handle); err != nil {
		return nil, err
	}

	length, err := pm.call(ctx, pm.inform, handle)
	if err != nil {
		return nil, err
	}
	if int64(length) > maxOutputBytes {
		return nil, &OutputLimitError{Limit: maxOutputBytes, Got: length}
	}
	ptr, err := pm.call(ctx, pm.lastPtr)
	if err != nil {
		return nil, err
	}
	view, ok := pm.module.Memory().Read(uint32(ptr), uint32(length))
	if !ok {
		return nil, fmt.Errorf("%w: report outside linear memory", ErrProbeFailed)
	}
	report := make([]byte, length)
	copy(report, view)
	return report, nil
}

// stream runs the Open_Buffer_Continue loop: feed chunks, honor MediaInfo's
// absolute GoTo requests as seeks, stop when MediaInfo is finalized or the
// file is exhausted.
func (pm *probeModule) stream(ctx context.Context, file *os.File, size int64, handle uint64, chunkPtr uint32) error {
	buffer := make([]byte, streamChunkSize)
	offset := int64(0)
	// Bound the loop: a parser bug must not spin forever. Sequential reading
	// needs at most size/chunk iterations; GoTo seeks get a generous extra
	// budget because container formats legitimately jump around.
	maxIterations := size/streamChunkSize + 4096

	for iteration := int64(0); ; iteration++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if iteration > maxIterations {
			return fmt.Errorf("%w: seek loop exceeded %d iterations", ErrProbeFailed, maxIterations)
		}

		n, readErr := file.ReadAt(buffer, offset)
		if n == 0 {
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return fmt.Errorf("%w: read failed: %v", ErrProbeFailed, readErr)
			}
			return nil
		}

		if !pm.module.Memory().Write(chunkPtr, buffer[:n]) {
			return fmt.Errorf("%w: chunk outside linear memory", ErrProbeFailed)
		}
		status, err := pm.call(ctx, pm.bufferContinue, handle, uint64(chunkPtr), uint64(n))
		if err != nil {
			return err
		}
		offset += int64(n)

		goTo, err := pm.call(ctx, pm.bufferGoToGet, handle)
		if err != nil {
			return err
		}
		if goTo != noGoTo {
			if goTo > uint64(1<<63-1) {
				return fmt.Errorf("%w: GoTo offset out of range", ErrProbeFailed)
			}
			offset = int64(goTo)
		} else if readErr == io.EOF {
			return nil
		}

		if status&statusFinalized != 0 {
			return nil
		}
	}
}
