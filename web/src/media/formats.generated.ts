// Generated from internal/formats by `ITEMBA_UPDATE_FORMATS=1 go test ./internal/formats/`.
// The format matrix: what the console presents per path. Do not edit; edit the Go table.
export interface FormatEntry {
  kind: 'video' | 'audio' | 'image' | 'document';
  format: string;
  label: string;
  extensions: string[];
  mimeTypes?: string[];
  relay?: 'player' | 'viewer' | 'reader';
  probe?: string;
  sandbox?: 'sample' | 'render';
  converter?: string;
  fingerprint?: string;
  note?: string;
}

export const FORMATS: FormatEntry[] = [
  {
    "kind": "video",
    "format": "mp4",
    "label": "MP4",
    "extensions": [
      "mp4",
      "m4v",
      "mov"
    ],
    "mimeTypes": [
      "video/mp4",
      "video/quicktime",
      "application/mp4"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "video-frames-v1",
    "note": "The browser's own decoder; codecs the browser lacks stay unplayable."
  },
  {
    "kind": "video",
    "format": "webm",
    "label": "WebM",
    "extensions": [
      "webm"
    ],
    "mimeTypes": [
      "video/webm"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "video-frames-v1"
  },
  {
    "kind": "video",
    "format": "mkv",
    "label": "Matroska",
    "extensions": [
      "mkv"
    ],
    "mimeTypes": [
      "video/x-matroska"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "video-frames-v1",
    "note": "Plays when the codecs inside are ones the browser decodes."
  },
  {
    "kind": "video",
    "format": "ogv",
    "label": "Ogg video",
    "extensions": [
      "ogv"
    ],
    "mimeTypes": [
      "video/ogg"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "video-frames-v1"
  },
  {
    "kind": "video",
    "format": "hls",
    "label": "HLS",
    "extensions": [
      "m3u8"
    ],
    "mimeTypes": [
      "application/vnd.apple.mpegurl",
      "application/x-mpegurl"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "video-frames-v1",
    "note": "Through hls.js, the lowest rendition; the relay rewrites the playlist onto itself."
  },
  {
    "kind": "video",
    "format": "ts",
    "label": "MPEG transport stream",
    "extensions": [
      "ts"
    ],
    "mimeTypes": [
      "video/mp2t"
    ],
    "note": "A segment of an HLS stream; plays only inside its playlist."
  },
  {
    "kind": "video",
    "format": "dash",
    "label": "DASH",
    "extensions": [
      "mpd"
    ],
    "mimeTypes": [
      "application/dash+xml"
    ],
    "note": "Reported as unsupported rather than attempted."
  },
  {
    "kind": "audio",
    "format": "mp3",
    "label": "MP3",
    "extensions": [
      "mp3"
    ],
    "mimeTypes": [
      "audio/mpeg"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1"
  },
  {
    "kind": "audio",
    "format": "flac",
    "label": "FLAC",
    "extensions": [
      "flac"
    ],
    "mimeTypes": [
      "audio/flac",
      "audio/x-flac"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1"
  },
  {
    "kind": "audio",
    "format": "ogg",
    "label": "Ogg audio",
    "extensions": [
      "ogg",
      "oga"
    ],
    "mimeTypes": [
      "audio/ogg"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1"
  },
  {
    "kind": "audio",
    "format": "opus",
    "label": "Opus",
    "extensions": [
      "opus"
    ],
    "mimeTypes": [
      "audio/opus"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1"
  },
  {
    "kind": "audio",
    "format": "aac",
    "label": "AAC in MP4",
    "extensions": [
      "m4a"
    ],
    "mimeTypes": [
      "audio/mp4",
      "audio/aac"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1",
    "note": "A trailing index decodes only in the player, not offline, so the fingerprint may be missing."
  },
  {
    "kind": "audio",
    "format": "wav",
    "label": "WAVE",
    "extensions": [
      "wav"
    ],
    "mimeTypes": [
      "audio/wav",
      "audio/x-wav",
      "audio/wave"
    ],
    "relay": "player",
    "probe": "media_probe",
    "sandbox": "sample",
    "fingerprint": "acoustic-bands-v1"
  },
  {
    "kind": "audio",
    "format": "weba",
    "label": "WebM audio",
    "extensions": [
      "weba"
    ],
    "mimeTypes": [
      "audio/webm"
    ],
    "relay": "player",
    "note": "The player decodes it; the audio probe and sample take the Archive's containers only."
  },
  {
    "kind": "image",
    "format": "jpeg",
    "label": "JPEG",
    "extensions": [
      "jpg",
      "jpeg"
    ],
    "mimeTypes": [
      "image/jpeg"
    ],
    "relay": "viewer",
    "probe": "image_decode",
    "sandbox": "render",
    "fingerprint": "image-dhash-v1"
  },
  {
    "kind": "image",
    "format": "png",
    "label": "PNG",
    "extensions": [
      "png"
    ],
    "mimeTypes": [
      "image/png"
    ],
    "relay": "viewer",
    "probe": "image_decode",
    "sandbox": "render",
    "fingerprint": "image-dhash-v1"
  },
  {
    "kind": "image",
    "format": "gif",
    "label": "GIF",
    "extensions": [
      "gif"
    ],
    "mimeTypes": [
      "image/gif"
    ],
    "relay": "viewer",
    "probe": "image_decode",
    "sandbox": "render",
    "fingerprint": "image-dhash-v1"
  },
  {
    "kind": "image",
    "format": "webp",
    "label": "WebP",
    "extensions": [
      "webp"
    ],
    "mimeTypes": [
      "image/webp"
    ],
    "relay": "viewer",
    "sandbox": "render",
    "note": "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."
  },
  {
    "kind": "image",
    "format": "avif",
    "label": "AVIF",
    "extensions": [
      "avif"
    ],
    "mimeTypes": [
      "image/avif"
    ],
    "relay": "viewer",
    "sandbox": "render",
    "note": "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."
  },
  {
    "kind": "image",
    "format": "bmp",
    "label": "BMP",
    "extensions": [
      "bmp"
    ],
    "mimeTypes": [
      "image/bmp"
    ],
    "relay": "viewer",
    "sandbox": "render",
    "note": "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."
  },
  {
    "kind": "image",
    "format": "tiff",
    "label": "TIFF",
    "extensions": [
      "tif",
      "tiff"
    ],
    "mimeTypes": [
      "image/tiff"
    ],
    "relay": "viewer",
    "note": "The probe cannot decode it; browsers seldom show it either."
  },
  {
    "kind": "image",
    "format": "svg",
    "label": "SVG",
    "extensions": [
      "svg"
    ],
    "mimeTypes": [
      "image/svg+xml"
    ],
    "note": "May carry script; never relayed to the viewer or the sandbox."
  },
  {
    "kind": "document",
    "format": "pdf",
    "label": "PDF",
    "extensions": [
      "pdf"
    ],
    "mimeTypes": [
      "application/pdf"
    ],
    "relay": "reader",
    "probe": "document_parse",
    "sandbox": "render",
    "fingerprint": "text-simhash-v2"
  },
  {
    "kind": "document",
    "format": "txt",
    "label": "Plain text",
    "extensions": [
      "txt"
    ],
    "mimeTypes": [
      "text/plain"
    ],
    "probe": "document_parse",
    "fingerprint": "text-simhash-v2",
    "note": "Read and fingerprinted; opens in the protected browser, not the reader."
  },
  {
    "kind": "document",
    "format": "epub",
    "label": "EPUB",
    "extensions": [
      "epub"
    ],
    "mimeTypes": [
      "application/epub+zip"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "ebook-convert",
    "note": "Converted to PDF first, by Calibre's ebook-convert."
  },
  {
    "kind": "document",
    "format": "mobi",
    "label": "Kindle (MOBI, AZW)",
    "extensions": [
      "mobi",
      "azw",
      "azw3"
    ],
    "mimeTypes": [
      "application/x-mobipocket-ebook"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "ebook-convert",
    "note": "Converted to PDF first, by Calibre's ebook-convert."
  },
  {
    "kind": "document",
    "format": "fb2",
    "label": "FictionBook",
    "extensions": [
      "fb2"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "ebook-convert",
    "note": "Converted to PDF first, by Calibre's ebook-convert."
  },
  {
    "kind": "document",
    "format": "lit",
    "label": "Microsoft Reader",
    "extensions": [
      "lit"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "ebook-convert",
    "note": "Converted to PDF first, by Calibre's ebook-convert."
  },
  {
    "kind": "document",
    "format": "djvu",
    "label": "DjVu",
    "extensions": [
      "djvu"
    ],
    "mimeTypes": [
      "image/vnd.djvu"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "ddjvu",
    "note": "Converted to PDF first, by DjVuLibre's ddjvu."
  },
  {
    "kind": "document",
    "format": "word",
    "label": "Word, OpenDocument text, RTF",
    "extensions": [
      "doc",
      "docx",
      "odt",
      "rtf"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "soffice",
    "note": "Converted to PDF first, by LibreOffice."
  },
  {
    "kind": "document",
    "format": "slides",
    "label": "PowerPoint, OpenDocument presentation",
    "extensions": [
      "ppt",
      "pptx",
      "odp"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "soffice",
    "note": "Converted to PDF first, by LibreOffice."
  },
  {
    "kind": "document",
    "format": "sheet",
    "label": "Excel, OpenDocument spreadsheet",
    "extensions": [
      "xls",
      "xlsx",
      "ods"
    ],
    "relay": "reader",
    "sandbox": "render",
    "converter": "soffice",
    "note": "Converted to PDF first, by LibreOffice."
  },
  {
    "kind": "document",
    "format": "html",
    "label": "HTML",
    "extensions": [
      "htm",
      "html"
    ],
    "mimeTypes": [
      "text/html"
    ],
    "note": "A page, not a document: the protected browser opens it."
  }
];
