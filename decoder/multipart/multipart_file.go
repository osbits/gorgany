package multipart

import (
	"fmt"
	"io"
	"mime/multipart"
	"reflect"
	"runtime/debug"
	"strings"

	grgErr "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/model"
)

// DecodeFiles binds the uploaded parts of a multipart form onto the matching fields of
// dest, returning the stored files so the caller can decide when to release them.
//
// This is the documented way to receive an upload — declare a core.IFile field on the DTO —
// and it used to be the path with no content check at all: the hand-rolled FormFile and
// GetFiles applied an allowlist (to the type the *client* declared, which is a separate
// problem), and this applied none. The check now lives in model.NewMultipartFile, where it
// runs off the sniffed content for every path into storage; a part whose type is not
// allowed fails here with model.UploadTypeError instead of being written and bound.
//
// The returned closers are the *stored* files, not the part readers. Each part reader is
// closed as soon as its content has been copied out, because nothing afterwards reads from
// it — the stored file reads from its own temp copy. The stored files, in contrast, must
// outlive this call: the field it was just bound to is what a handler publishes with Write,
// and Write reads the temp copy. See MultipartParser.releaseWithRequest.
func DecodeFiles(
	filesMap map[string][]*multipart.FileHeader, dest any) (stored []io.Closer, err error) {

	// The destination's shape is checked before any reflection that can panic on it, so a
	// hostile or simply wrong `dest` produces an error rather than a 500.
	reflectedDestVal := reflect.ValueOf(dest)
	if !reflectedDestVal.IsValid() ||
		reflectedDestVal.Kind() != reflect.Pointer ||
		reflectedDestVal.IsNil() {
		return nil, fmt.Errorf("multipart: destination must be a non-nil pointer, got %T", dest)
	}
	target := reflectedDestVal.Elem()
	if target.Kind() != reflect.Struct {
		return nil, fmt.Errorf(
			"multipart: destination must point to a struct, got a pointer to %s", target.Kind())
	}

	defer releaseOnFailure(&stored, &err)

	stored = make([]io.Closer, 0)
	for key, files := range filesMap {
		if len(files) == 0 {
			continue
		}

		field := target.FieldByNameFunc(func(n string) bool {
			return strings.EqualFold(key, n)
		})

		if !field.IsValid() {
			continue
		}

		// Both checks happen here, before the part is opened and before anything is written
		// to disk. They used to happen implicitly, at the field.Set below — after the upload
		// had already been stored — and reflect signals both by panicking. A panic unwinds
		// without returning, so the closer for the file just written, and for every file
		// stored earlier in this loop, never reached the caller: RecoveryMiddleware turned it
		// into a 500 and the process carried on, which made it an unauthenticated, repeatable
		// way to fill the disk.
		//
		// CanSet is checked first and separately from assignability. An unexported field
		// matched by EqualFold is IsValid and not CanSet, and AssignableTo would happily say
		// yes for an unexported core.IFile — so an assignability-only check leaves that panic
		// open.
		if !field.CanSet() {
			return stored, &FieldBindError{Field: key, Reason: fieldNotAFileUpload}
		}
		if !multipartFileType.AssignableTo(field.Type()) {
			return stored, &FieldBindError{Field: key, Reason: fieldNotAFileUpload}
		}

		rawFile := files[0]
		reader, openErr := rawFile.Open()
		if openErr != nil {
			return stored, openErr
		}

		file, storeErr := model.NewMultipartFile(rawFile.Filename, reader)
		reader.Close()
		if storeErr != nil {
			// The error used to be dropped on the floor here, and the nil *MultipartFile
			// assigned anyway — which produces a non-nil core.IFile holding a nil pointer,
			// so the handler's first call on it panicked somewhere unrelated to the upload.
			return stored, fmt.Errorf("failed to store upload for field %q: %w", key, storeErr)
		}

		stored = append(stored, file)

		field.Set(reflect.ValueOf(file))
	}

	return stored, nil
}

// multipartFileType is what a DTO field has to be able to hold.
var multipartFileType = reflect.TypeOf((*model.MultipartFile)(nil))

// fieldNotAFileUpload is the reason a client is given.
//
// Deliberately neutral. The sibling parser's equivalent says "Cannot assign value of type %s
// to type %s" with reflect.Type values in it, which hands a client the framework's internal
// type names; a client can do nothing with those, and an attacker can.
const fieldNotAFileUpload = "This field does not accept a file upload"

// FieldBindError reports a part that names a field the DTO cannot hold.
//
// A local type rather than the http package's newValidationError, which is package-private
// there and would pull this decoder into an import cycle. The HTTP layer maps it — see
// MultipartParser.Parse — so the client gets the standard validation envelope naming the one
// field that was wrong, rather than the old fallback's one reason fanned across every part.
type FieldBindError struct {
	Field  string
	Reason string
}

func (e *FieldBindError) Error() string {
	return fmt.Sprintf("multipart: field %q: %s", e.Field, e.Reason)
}

// releaseOnFailure closes every temp copy this call created, on any error and on any panic.
//
// The named return values are load-bearing: this has to see, and be able to replace, what the
// function actually returns.
//
// Recovering is a backstop and not the mechanism — the two known panics are closed by the
// explicit checks above. It is here because *the leak, not the panic, is the security defect*:
// reflect has panic sources those checks do not enumerate, and any one of them would re-open
// unauthenticated repeatable disk fill. Recovering turns "orphan every file written so far"
// into "release every file written so far, and return a 4xx".
func releaseOnFailure(stored *[]io.Closer, err *error) {
	if recovered := recover(); recovered != nil {
		*err = &FieldBindError{Reason: "This upload could not be bound to the request"}
		grgErr.HandleError(fmt.Errorf(
			"multipart: recovered while binding uploads: %v\n%s", recovered, debug.Stack()))
	}

	if *err == nil {
		return
	}

	// MultipartFile.Close is idempotent, so the caller closing these again is harmless — and
	// MultipartParser.Parse deliberately still does, as a second layer.
	for _, closer := range *stored {
		_ = closer.Close()
	}
	*stored = nil
}
