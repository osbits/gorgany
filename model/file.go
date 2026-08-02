package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime"
	nethttp "net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/spf13/viper"
)

const (
	TempStorage   = "resource/temp"
	PublicStorage = "resource/public"
)

// ConfigAllowedUploadTypes names the config key holding the upload allowlist: a map from
// a *sniffed* media type to the file extension an upload of that type is stored with.
// Setting it replaces the built-in table wholesale, so an app that needs one extra type
// has to restate the ones it still wants.
const ConfigAllowedUploadTypes = "http.upload.allowedTypes"

// uploadSniffLength is how many bytes http.DetectContentType inspects. Reading exactly
// this many keeps the sniff buffer the same size as the decision it feeds.
const uploadSniffLength = 512

// maxStoredBaseNameLength bounds how much of the client's filename survives into the
// stored name.
//
// Everything else in that name is generated here, so its length is known; the base was
// passed through at whatever length arrived. A filesystem path component is limited to 255
// bytes almost everywhere, so a filename longer than that made os.Create fail with "file
// name too long" and the upload was refused — which is an ordinary browser upload from
// someone with a verbose filename, not an attack. The bound is well under the limit
// because the unique prefix and the extension have to fit alongside it, and because the
// base name is a convenience for whoever reads the directory rather than an identifier.
const maxStoredBaseNameLength = 96

// maxUploadExtensionLength bounds a configured extension. Nothing real is longer, and a
// value this far from an extension is a configuration mistake worth surfacing.
const maxUploadExtensionLength = 16

// defaultAllowedUploadTypes maps the media types an upload may have to the extension it
// is stored under.
//
// Both halves of that matter, and the second one is the fix. The stored name used to keep
// whatever extension the client's filename carried — `filepath.Ext(originalFileName)` was
// appended verbatim while only the base name was sanitised — so a part named
// `payload.html` was written to public storage as `…-payload.html`. The public file server
// then derived its Content-Type from that extension, and the upload came back as
// `text/html` from the application's own origin, where script in it is same-origin and can
// read every token the app publishes to its own pages. `.svg` did the same through
// `image/svg+xml`. Declaring `Content-Type: image/png` on the part was enough to satisfy
// the old allowlist, because that header is written by the client.
//
// So the extension is derived from the bytes instead, and a type with no entry here is
// refused rather than stored under a name nobody vouched for. text/html, image/svg+xml and
// the XML types are absent deliberately: they are script hosts, and there is no extension
// this framework can hand them that makes them safe to serve back.
//
// application/octet-stream is present, and it is the entry that keeps this from breaking
// working apps: http.DetectContentType answers with it for any binary format it has no
// signature for, so an upload of some format nobody anticipated is stored as `.bin` rather
// than rejected. `.bin` is inert — the public file server serves it as a
// non-renderable attachment — which is the whole point.
var defaultAllowedUploadTypes = map[string]string{
	"image/png":                    ".png",
	"image/jpeg":                   ".jpg",
	"image/gif":                    ".gif",
	"image/webp":                   ".webp",
	"image/bmp":                    ".bmp",
	"image/tiff":                   ".tiff",
	"image/vnd.microsoft.icon":     ".ico",
	"application/pdf":              ".pdf",
	"application/zip":              ".zip",
	"application/x-gzip":           ".gz",
	"application/x-rar-compressed": ".rar",
	"application/ogg":              ".ogg",
	"application/wasm":             ".wasm",
	"application/font-woff":        ".woff",
	"application/x-font-ttf":       ".ttf",
	"audio/mpeg":                   ".mp3",
	"audio/wave":                   ".wav",
	"audio/aiff":                   ".aiff",
	"audio/midi":                   ".mid",
	"audio/basic":                  ".au",
	"video/mp4":                    ".mp4",
	"video/webm":                   ".webm",
	"video/avi":                    ".avi",
	"text/plain":                   ".txt",
	"application/octet-stream":     ".bin",
}

// UploadTypeError is what an upload whose sniffed media type is not on the allowlist fails
// with. It carries the type rather than the bytes, so it is safe to log and to render.
type UploadTypeError struct {
	// FileName is the name the client gave the part, for the operator reading the log.
	FileName string
	// MediaType is the type the content actually sniffed as.
	MediaType string
}

func (thiz *UploadTypeError) Error() string {
	return fmt.Sprintf("upload %q has content of type %s, which is not an allowed upload type",
		thiz.FileName, thiz.MediaType)
}

// UploadTypePolicy decides whether an upload may be stored and under which extension.
type UploadTypePolicy struct {
	extensionByType map[string]string
}

// ResolveUploadTypePolicy reads the allowlist from config, falling back to the built-in
// table. A configured map that turns out to be empty falls back too: an accidentally blank
// config value must not be read as "refuse every upload".
func ResolveUploadTypePolicy() UploadTypePolicy {
	configured := viper.GetStringMapString(ConfigAllowedUploadTypes)
	if len(configured) == 0 {
		return UploadTypePolicy{extensionByType: defaultAllowedUploadTypes}
	}

	normalised := make(map[string]string, len(configured))
	for mediaType, extension := range configured {
		mediaType = strings.ToLower(strings.TrimSpace(mediaType))
		extension, ok := normaliseUploadExtension(extension)
		if mediaType == "" || !ok {
			continue
		}
		normalised[mediaType] = extension
	}
	if len(normalised) == 0 {
		return UploadTypePolicy{extensionByType: defaultAllowedUploadTypes}
	}

	return UploadTypePolicy{extensionByType: normalised}
}

// normaliseUploadExtension turns a configured value into an extension, and reports whether
// it is one at all.
//
// This is the tightest check in the file because the extension is now the whole of the
// upload path's promise: the stored name's extension is what the public file server derives
// a Content-Type from, and the name itself is joined onto both the temp and the public
// directory. A value taken verbatim was a path — an entry of "../../pwned.html" put the
// temp copy a directory above resource/temp under an extension nothing had approved, and
// MultipartFile.Write joins the same name onto the public root — and a value carrying a
// newline was a stored filename that could try to write a response header of its own. So
// only a dot and ASCII alphanumerics get through; anything else is a configuration mistake
// and the entry is dropped rather than obeyed.
func normaliseUploadExtension(extension string) (string, bool) {
	extension = strings.TrimSpace(extension)
	extension = strings.TrimPrefix(extension, ".")
	if extension == "" || len(extension) > maxUploadExtensionLength {
		return "", false
	}

	for _, r := range extension {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return "", false
	}

	return "." + extension, true
}

// ExtensionFor reports the extension an upload of mediaType is stored under, and whether
// the type is allowed at all.
func (thiz UploadTypePolicy) ExtensionFor(mediaType string) (string, bool) {
	extension, ok := thiz.extensionByType[strings.ToLower(mediaType)]
	return extension, ok
}

// DetectUploadMediaType sniffs head and returns the media type without its parameters, so
// callers can compare against a plain `text/plain` rather than against whichever charset
// the sniffer happened to append.
func DetectUploadMediaType(head []byte) string {
	detected := nethttp.DetectContentType(head)
	if base, _, err := mime.ParseMediaType(detected); err == nil {
		return base
	}
	return detected
}

// ensureDirectories creates necessary directories if they don't exist
func ensureDirectories() error {
	dirs := []string{TempStorage, PublicStorage}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// NewMultipartFile stores reader's content as a request-scoped temp file, under a name
// whose extension is derived from what the content sniffs as. See
// defaultAllowedUploadTypes for why the client's filename is not trusted with that
// decision, and UploadTypePolicy for how an app widens the allowlist.
func NewMultipartFile(originalFileName string, reader io.Reader) (*MultipartFile, error) {
	return newMultipartFile(originalFileName, reader, ResolveUploadTypePolicy())
}

func newMultipartFile(originalFileName string, reader io.Reader, policy UploadTypePolicy) (*MultipartFile, error) {
	if reader == nil {
		return nil, errors.New("no upload content to store")
	}

	// The type is settled before anything is written, so a refused upload leaves nothing
	// behind on disk.
	//
	// Sniffing consumes the front of the reader, and a multipart part is a one-shot stream
	// with no Seek — so the head is kept and put back in front of the rest with
	// io.MultiReader below. Dropping it would silently truncate every upload by up to 512
	// bytes, which is why the read and the copy have to be written as one thing.
	head := make([]byte, uploadSniffLength)
	read, readErr := io.ReadFull(reader, head)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("failed to read upload: %w", readErr)
	}
	head = head[:read]

	if read == 0 {
		// DetectContentType answers "text/plain" for no bytes at all, which would store an
		// empty upload as a .txt file and report success for a part that carried nothing.
		return nil, fmt.Errorf("upload %q is empty", originalFileName)
	}

	mediaType := DetectUploadMediaType(head)
	ext, allowed := policy.ExtensionFor(mediaType)
	if !allowed {
		return nil, &UploadTypeError{FileName: originalFileName, MediaType: mediaType}
	}

	if err := ensureDirectories(); err != nil {
		return nil, err
	}

	// Generate a unique filename using timestamp and random number
	timestamp := time.Now().UnixNano()
	random := rand.Intn(10000)
	uniqueId := fmt.Sprintf("%d%d", timestamp, random)

	// Sanitize the original filename. Only the base name survives from the client: the
	// extension comes from the sniffed type, and the mapping below turns any '.' left in
	// the base into '-' so a name like `logo.php.png` cannot smuggle a second extension in.
	baseName := strings.TrimSuffix(originalFileName, filepath.Ext(originalFileName))
	sanitizedName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, baseName)
	// The mapping above leaves nothing but ASCII behind, so a byte cut is a rune cut.
	if len(sanitizedName) > maxStoredBaseNameLength {
		sanitizedName = sanitizedName[:maxStoredBaseNameLength]
	}
	if strings.Trim(sanitizedName, "-_") == "" {
		sanitizedName = "upload"
	}

	fileName := fmt.Sprintf("%s-%s%s", uniqueId, sanitizedName, ext)

	file := MultipartFile{mediaType: mediaType}
	file.SetName(fileName)

	// Create temp file
	tempPath := filepath.Join(TempStorage, fileName)
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Copy content to temp file
	if _, err := io.Copy(tempFile, io.MultiReader(bytes.NewReader(head), reader)); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}

	// Ensure the file is written to disk
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Reset file position for future reads
	if _, err := tempFile.Seek(0, 0); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	file.tempFile = tempFile
	file.tempPath = tempPath
	return &file, nil
}

type AbstractFile struct {
	Name string
	Path string
}

func (thiz *AbstractFile) SetName(name string) {
	thiz.Name = name
}

func (thiz *AbstractFile) GetName() string {
	return thiz.Name
}

func (thiz *AbstractFile) GetPath() string {
	return thiz.Path
}

func (thiz *AbstractFile) GetSize() (int64, error) {
	info, err := os.Stat(thiz.FullPath())
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

func (thiz *AbstractFile) FullPath() string {
	if thiz.Path == "" {
		return ""
	}
	return path.Join(PublicStorage, thiz.Path, thiz.Name)
}

// MultipartFile - this structure describes file which has been gotten by http, it can be stored on project server
type MultipartFile struct {
	AbstractFile
	tempFile *os.File
	// tempPath is where the temp copy lives, recorded rather than recomputed. Close used
	// to resolve its own target through getActualFilePath, which returns the *public* path
	// once Write has set one — so closing a file the handler had already published deleted
	// the published copy. That is why the request-scoped cleanup was commented out.
	tempPath string
	// mediaType is what the content sniffed as when the file was accepted.
	mediaType string
}

// MediaType returns the media type the upload's own bytes sniffed as, with no parameters
// — `image/png`, not `image/png; charset=…`. It is not the type the client declared, which
// is why callers applying their own allowlist should use this.
func (thiz *MultipartFile) MediaType() string {
	return thiz.mediaType
}

func (thiz *MultipartFile) SetName(name string) {
	thiz.Name = name
}

func (thiz *MultipartFile) GetName() string {
	return thiz.Name
}

func (thiz *MultipartFile) GetPath() string {
	return thiz.Path
}

func (thiz *MultipartFile) GetSize() (int64, error) {
	actualFilePath := thiz.getActualFilePath()
	if actualFilePath == "" {
		return 0, fmt.Errorf("no file path")
	}

	info, err := os.Stat(actualFilePath)
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

func (thiz *MultipartFile) Read(writer io.Writer) (int64, error) {
	actualFilePath := thiz.getActualFilePath()
	if actualFilePath == "" {
		return 0, fmt.Errorf("no file path")
	}

	file, err := os.Open(actualFilePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	return io.Copy(writer, file)
}

func (thiz *MultipartFile) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}

	// Write reads the temp copy when there is one and the caller's reader otherwise, and
	// with neither it used to dereference the nil reader inside io.Copy. The reachable shape
	// is a file that has already been released: MultipartParser closes the stored uploads
	// straight away for a message that does not take part in request-scoped cleanup, and a
	// handler that then publishes with Write(path, nil) took the process down instead of
	// being told its upload was gone.
	if thiz.tempFile == nil && reader == nil {
		return 0, errors.New("nothing to write: the upload has no temp copy and no reader was supplied")
	}

	// Ensure target directory exists
	targetDir := path.Join(PublicStorage, p)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create target directory: %w", err)
	}

	// If file already exists in a different location, delete it
	if thiz.Path != "" && thiz.Path != p {
		if err := thiz.Delete(); err != nil {
			return 0, fmt.Errorf("failed to delete existing file: %w", err)
		}
	}

	thiz.Path = p
	targetPath := path.Join(targetDir, thiz.Name)

	// Create the target file
	targetFile, err := os.Create(targetPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create target file: %w", err)
	}
	defer targetFile.Close()

	// Copy content
	var written int64
	if thiz.tempFile != nil {
		// Read from temp file
		if _, err := thiz.tempFile.Seek(0, 0); err != nil {
			return 0, fmt.Errorf("failed to seek temp file: %w", err)
		}
		written, err = io.Copy(targetFile, thiz.tempFile)
	} else {
		// Read from provided reader
		written, err = io.Copy(targetFile, reader)
	}

	if err != nil {
		os.Remove(targetPath) // Clean up on error
		return 0, fmt.Errorf("failed to write file: %w", err)
	}

	// Ensure the file is written to disk
	if err := targetFile.Sync(); err != nil {
		os.Remove(targetPath) // Clean up on error
		return 0, fmt.Errorf("failed to sync file: %w", err)
	}

	return written, nil
}

func (thiz *MultipartFile) Writer() (io.WriteCloser, error) {
	return os.Open(thiz.FullPath())
}

func (thiz *MultipartFile) FullPath() string {
	if thiz.Path == "" {
		return ""
	}
	return path.Join(PublicStorage, thiz.Path, thiz.Name)
}

// PublicPath is the URL this file is served at.
//
// One `public` segment, and a leading slash. It used to return "public/<Path>/<Name>" while
// the controller mapped /public/X onto resource/X — so the URL that actually worked for a
// stored upload was /public/public/<Path>/<Name>, and the value this returns was a 404. With
// the controller anchored at PublicStorage the two now round-trip exactly once:
//
//	bytes at  resource/public/avatars/x.png   (FullPath)
//	URL       /public/avatars/x.png           (PublicPath)
//
// The leading slash matters as much as the segment count. This value goes straight into an
// HTML attribute (view/cp/fields_params_builder.go) and into JSON (File.MarshalJSON); without
// it the URL is relative and resolves against whatever path the current document is at, so it
// was already wrong on every page not served from the root.
//
// Nothing stored needs migrating. File.Value persists path.Join(Path, Name) and File.Scan
// reads it back the same way — every URL is computed at render time, so only generated URLs
// change, and they change from broken to working.
func (thiz *MultipartFile) PublicPath() string {
	if thiz.Name == "" {
		return ""
	}
	return "/" + path.Join("public", thiz.Path, thiz.Name)
}

func (thiz *MultipartFile) IsExists() bool {
	if thiz.FullPath() == "" {
		return false
	}
	_, err := os.Stat(thiz.FullPath())
	return err == nil
}

func (thiz *MultipartFile) Delete() error {
	if thiz.Name == "" {
		return errors.New("file name is empty")
	}

	if !thiz.IsExists() {
		return errors.New("file does not exist")
	}

	return os.Remove(thiz.FullPath())
}

// Close releases the temp copy of the upload and deletes it.
//
// It is deliberately idempotent and deliberately narrow. Two things were wrong before:
// it resolved the file to delete through getActualFilePath, which returns the *public*
// path as soon as Write has published the upload — so a request-scoped cleanup would have
// deleted the stored file — and it closed the handle before asking for the path, so
// getActualFilePath's Stat on the now-closed handle failed and the temp copy was in fact
// never removed at all. Between the two, whoever wrote the cleanup that calls this had to
// choose between leaking every upload and destroying every upload, and left a todo.
//
// Being idempotent matters because both a handler and the request-scoped cleanup may call
// it, in either order.
func (thiz *MultipartFile) Close() error {
	if thiz.tempFile == nil {
		return nil
	}

	closeErr := thiz.tempFile.Close()
	thiz.tempFile = nil

	tempPath := thiz.tempPath
	thiz.tempPath = ""

	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return fmt.Errorf("failed to close temp file: %w", closeErr)
	}

	if tempPath == "" {
		return nil
	}

	if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove temp file: %w", err)
	}

	return nil
}

// getActualFilePath - returns full path for temp file if permanent does not exist
func (thiz *MultipartFile) getActualFilePath() string {
	if thiz.FullPath() == "" {
		return thiz.tempPath
	}

	return thiz.FullPath()
}

// File - this structure describes File which stores in project server storage and links with record in db
func NewFileFromMultipart(multipartFile MultipartFile) *File {
	return &File{
		MultipartFile: multipartFile,
	}
}

type File struct {
	MultipartFile
}

func (thiz *File) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}

	if thiz.FullPath() != "" && thiz.FullPath() != path.Join(PublicStorage, thiz.Path, thiz.Name) {
		err := thiz.Delete()
		if err != nil {
			return 0, err
		}
	}

	thiz.Path = p

	err := os.MkdirAll(path.Join(PublicStorage, thiz.Path), os.ModePerm)
	if err != nil {
		return 0, err
	}

	rawFile, err := os.Create(path.Join(PublicStorage, thiz.Path, thiz.Name))
	if err != nil {
		return 0, err
	}

	//thiz.StoredFilePath = path.Join(PublicStorage, thiz.Path, thiz.Name)

	return io.Copy(rawFile, reader)
}

func (thiz *File) Scan(value interface{}) error {
	fullFilePath, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to cast value:", value))
	}

	splitFullFilePath := strings.Split(fullFilePath, "/")
	fileName := splitFullFilePath[len(splitFullFilePath)-1]
	p := strings.Join(splitFullFilePath[:len(splitFullFilePath)-1], "/")

	thiz.Path = p
	thiz.Name = fileName

	return nil
}

func (thiz File) Value() (driver.Value, error) {
	if thiz.Path == "" || thiz.Name == "" {
		return nil, nil
	}
	return path.Join(thiz.Path, thiz.Name), nil
}

func (thiz File) MarshalJSON() ([]byte, error) {
	if thiz.Name == "" {
		return []byte("null"), nil
	}

	size, err := thiz.GetSize()
	if err != nil {
		err2.HandleError(err)
		return []byte("null"), nil
	}

	fileMap := make(map[string]any)
	fileMap["name"] = thiz.Name
	fileMap["size"] = size
	fileMap["path"] = thiz.PublicPath()

	jsonFile, err := json.Marshal(fileMap)
	if err != nil {
		return nil, nil
	}
	return jsonFile, nil
}

func (thiz *File) Close() error {
	return nil
}
