// bridge.cpp - WASI reactor exports around MediaInfoLib for Bansoko.
//
// Bansoko probes media through the MediaInfo buffer API: the Go runtime feeds
// file chunks in, MediaInfo asks for seeks via Open_Buffer_Continue_GoTo_Get,
// and the final report is read back as UTF-8 JSON. This translation unit is
// the only code we add on top of the pinned upstream trees; everything it
// calls is stock MediaInfoLib.
//
// The module is built with -mexec-model=reactor, so wazero instantiates it
// once per probe, runs _initialize, and then calls these exports. Each
// instance owns its MediaInfo objects, which keeps concurrent probes free of
// shared state.

#include <cstdint>
#include <cstdlib>
#include <new>
#include <string>

#include "MediaInfo/MediaInfo.h"
#include "ZenLib/Ztring.h"

namespace {

// MediaInfoLib strings are wchar_t based. wasi-libc uses 32-bit wchar_t, so
// each element is a full Unicode code point and a hand-rolled conversion is
// exact (surrogates cannot appear in UCS-4 data).
std::string ToUtf8(const MediaInfoLib::String& value) {
    std::string out;
    out.reserve(value.size());
    for (wchar_t wc : value) {
        const std::uint32_t cp = static_cast<std::uint32_t>(wc);
        if (cp < 0x80) {
            out += static_cast<char>(cp);
        } else if (cp < 0x800) {
            out += static_cast<char>(0xC0 | (cp >> 6));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        } else if (cp < 0x10000) {
            out += static_cast<char>(0xE0 | (cp >> 12));
            out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        } else {
            out += static_cast<char>(0xF0 | (cp >> 18));
            out += static_cast<char>(0x80 | ((cp >> 12) & 0x3F));
            out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        }
    }
    return out;
}

MediaInfoLib::MediaInfo* Handle(std::uint32_t h) {
    return reinterpret_cast<MediaInfoLib::MediaInfo*>(static_cast<std::uintptr_t>(h));
}

// Result channel for string-returning exports: the export stores the UTF-8
// bytes here and returns their length; the caller reads them from
// mi_last_ptr(). One instance serves one probe at a time, so a single buffer
// is enough.
std::string g_last;

} // namespace

extern "C" {

#define MI_EXPORT(name) __attribute__((export_name(#name)))

MI_EXPORT(mi_new)
std::uint32_t mi_new() {
    auto* mi = new (std::nothrow) MediaInfoLib::MediaInfo();
    if (mi == nullptr) {
        return 0;
    }
    return static_cast<std::uint32_t>(reinterpret_cast<std::uintptr_t>(mi));
}

MI_EXPORT(mi_delete)
void mi_delete(std::uint32_t h) {
    delete Handle(h);
}

// Selects JSON output, equivalent to `mediainfo --Output=JSON`.
MI_EXPORT(mi_set_output_json)
std::uint32_t mi_set_output_json(std::uint32_t h) {
    if (h == 0) {
        return 0;
    }
    // The library is compiled with -fno-exceptions, so calls cannot unwind.
    Handle(h)->Option(__T("Output"), __T("JSON"));
    return 1;
}

MI_EXPORT(mi_open_buffer_init)
std::uint32_t mi_open_buffer_init(std::uint32_t h, std::uint64_t file_size) {
    if (h == 0) {
        return 0;
    }
    return static_cast<std::uint32_t>(Handle(h)->Open_Buffer_Init(file_size, 0));
}

// Feeds the next chunk located at ptr/len inside the module's linear memory.
// Returns MediaInfoLib's status bitfield (bit 0 accepted, bit 1 filled).
MI_EXPORT(mi_open_buffer_continue)
std::uint32_t mi_open_buffer_continue(std::uint32_t h, std::uint32_t ptr, std::uint32_t len) {
    if (h == 0) {
        return 0;
    }
    const auto* buffer = reinterpret_cast<const ZenLib::int8u*>(static_cast<std::uintptr_t>(ptr));
    return static_cast<std::uint32_t>(Handle(h)->Open_Buffer_Continue(buffer, static_cast<size_t>(len)));
}

// Returns the absolute file offset MediaInfo wants next, or (uint64)-1 when
// sequential reading should continue.
MI_EXPORT(mi_open_buffer_goto_get)
std::uint64_t mi_open_buffer_goto_get(std::uint32_t h) {
    if (h == 0) {
        return static_cast<std::uint64_t>(-1);
    }
    return static_cast<std::uint64_t>(Handle(h)->Open_Buffer_Continue_GoTo_Get());
}

MI_EXPORT(mi_open_buffer_finalize)
std::uint32_t mi_open_buffer_finalize(std::uint32_t h) {
    if (h == 0) {
        return 0;
    }
    return static_cast<std::uint32_t>(Handle(h)->Open_Buffer_Finalize());
}

// Fills the result channel with the JSON report and returns its length.
MI_EXPORT(mi_inform)
std::uint32_t mi_inform(std::uint32_t h) {
    if (h == 0) {
        g_last.clear();
        return 0;
    }
    g_last = ToUtf8(Handle(h)->Inform());
    return static_cast<std::uint32_t>(g_last.size());
}

// Fills the result channel with the MediaInfoLib version string (self-check).
MI_EXPORT(mi_info_version)
std::uint32_t mi_info_version() {
    g_last = ToUtf8(MediaInfoLib::MediaInfo::Option_Static(__T("Info_Version")));
    return static_cast<std::uint32_t>(g_last.size());
}

// Pointer to the bytes of the last string returned by mi_inform /
// mi_info_version. Valid until the next string-returning call.
MI_EXPORT(mi_last_ptr)
std::uint32_t mi_last_ptr() {
    return static_cast<std::uint32_t>(reinterpret_cast<std::uintptr_t>(g_last.data()));
}

// Scratch allocation inside the module's linear memory for file chunks.
MI_EXPORT(mi_alloc)
std::uint32_t mi_alloc(std::uint32_t size) {
    void* p = std::malloc(size == 0 ? 1 : size);
    return static_cast<std::uint32_t>(reinterpret_cast<std::uintptr_t>(p));
}

MI_EXPORT(mi_free)
void mi_free(std::uint32_t ptr) {
    std::free(reinterpret_cast<void*>(static_cast<std::uintptr_t>(ptr)));
}

} // extern "C"
