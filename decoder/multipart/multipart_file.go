package multipart

import (
	"fmt"
	"io"
	"mime/multipart"
	"reflect"
	"strings"

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
func DecodeFiles(filesMap map[string][]*multipart.FileHeader, dest any) ([]io.Closer, error) {
	reflectedDestVal := reflect.ValueOf(dest)

	storedFiles := make([]io.Closer, 0)
	for key, files := range filesMap {
		if len(files) == 0 {
			continue
		}

		field := reflectedDestVal.Elem().FieldByNameFunc(func(n string) bool {
			return strings.EqualFold(key, n)
		})

		if !field.IsValid() {
			continue
		}

		rawFile := files[0]
		reader, err := rawFile.Open()
		if err != nil {
			return storedFiles, err
		}

		file, err := model.NewMultipartFile(rawFile.Filename, reader)
		reader.Close()
		if err != nil {
			// The error used to be dropped on the floor here, and the nil *MultipartFile
			// assigned anyway — which produces a non-nil core.IFile holding a nil pointer,
			// so the handler's first call on it panicked somewhere unrelated to the upload.
			return storedFiles, fmt.Errorf("failed to store upload for field %q: %w", key, err)
		}

		storedFiles = append(storedFiles, file)

		field.Set(reflect.ValueOf(file))
	}

	return storedFiles, nil
}
