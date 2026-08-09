package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"omni_money/backend/models"

	_ "golang.org/x/image/webp"
)

const maxImageFilenameBytes = 255

var imageMIMEByExtension = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

var imageFormatByMIME = map[string]string{
	"image/jpeg": "jpeg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

type preparedTransactionImage struct {
	filename string
	mimeType string
	data     []byte
}

func prepareTransactionImages(images []models.TransactionImageRequest) ([]preparedTransactionImage, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > models.MaxImagesPerTransaction {
		return nil, fmt.Errorf("画像は1取引につき%d件までです", models.MaxImagesPerTransaction)
	}

	prepared := make([]preparedTransactionImage, 0, len(images))
	var totalBytes int64
	for i, request := range images {
		image, err := prepareTransactionImage(request)
		if err != nil {
			return nil, fmt.Errorf("画像%d: %w", i+1, err)
		}
		totalBytes += int64(len(image.data))
		if totalBytes > models.MaxImageBytesPerTransaction {
			return nil, fmt.Errorf("画像データの合計は1取引につき%d MiBまでです", models.MaxImageBytesPerTransaction/(1024*1024))
		}
		prepared = append(prepared, image)
	}
	return prepared, nil
}

func prepareTransactionImage(request models.TransactionImageRequest) (preparedTransactionImage, error) {
	filename, mimeType, err := normalizeImageMetadata(request.Filename, request.MimeType)
	if err != nil {
		return preparedTransactionImage{}, err
	}
	encoded := strings.TrimSpace(request.Data)
	if encoded == "" || strings.ContainsFunc(encoded, unicode.IsSpace) {
		return preparedTransactionImage{}, fmt.Errorf("Base64データが無効です")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(int(models.MaxImageBytes)) {
		return preparedTransactionImage{}, fmt.Errorf("画像は1件につき%d MiBまでです", models.MaxImageBytes/(1024*1024))
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return preparedTransactionImage{}, fmt.Errorf("Base64データが無効です")
	}
	if int64(len(data)) > models.MaxImageBytes {
		return preparedTransactionImage{}, fmt.Errorf("画像は1件につき%d MiBまでです", models.MaxImageBytes/(1024*1024))
	}
	return validateDecodedTransactionImage(filename, mimeType, data)
}

func prepareDecodedTransactionImage(rawFilename, rawMIMEType string, data []byte) (preparedTransactionImage, error) {
	filename, mimeType, err := normalizeImageMetadata(rawFilename, rawMIMEType)
	if err != nil {
		return preparedTransactionImage{}, err
	}
	return validateDecodedTransactionImage(filename, mimeType, data)
}

func normalizeImageMetadata(rawFilename, rawMIMEType string) (string, string, error) {
	filename := strings.TrimSpace(rawFilename)
	if err := validateImageFilename(filename); err != nil {
		return "", "", err
	}
	expectedMIME, ok := imageMIMEByExtension[strings.ToLower(filepath.Ext(filename))]
	if !ok {
		return "", "", fmt.Errorf("拡張子はJPEG、PNG、GIF、WebPのみ使用できます")
	}

	mimeType := strings.ToLower(strings.TrimSpace(rawMIMEType))
	if mimeType == "" {
		mimeType = expectedMIME
	}
	if mimeType != expectedMIME {
		return "", "", fmt.Errorf("MIMEタイプと拡張子が一致しません")
	}
	return filename, mimeType, nil
}

func validateDecodedTransactionImage(filename, mimeType string, data []byte) (preparedTransactionImage, error) {
	if len(data) == 0 {
		return preparedTransactionImage{}, fmt.Errorf("画像データが空です")
	}
	if int64(len(data)) > models.MaxImageBytes {
		return preparedTransactionImage{}, fmt.Errorf("画像は1件につき%d MiBまでです", models.MaxImageBytes/(1024*1024))
	}

	detectedMIME := http.DetectContentType(data)
	if detectedMIME != mimeType {
		return preparedTransactionImage{}, fmt.Errorf("画像内容、MIMEタイプ、拡張子が一致しません")
	}
	if err := validateImageContainer(mimeType, data); err != nil {
		return preparedTransactionImage{}, err
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != imageFormatByMIME[mimeType] {
		return preparedTransactionImage{}, fmt.Errorf("画像ヘッダーをデコードできません")
	}
	if err := validateImageDimensions(config.Width, config.Height); err != nil {
		return preparedTransactionImage{}, err
	}

	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != imageFormatByMIME[mimeType] {
		return preparedTransactionImage{}, fmt.Errorf("画像データが破損しています")
	}
	bounds := decoded.Bounds()
	if err := validateImageDimensions(bounds.Dx(), bounds.Dy()); err != nil {
		return preparedTransactionImage{}, err
	}

	return preparedTransactionImage{filename: filename, mimeType: mimeType, data: data}, nil
}

func validateImageFilename(filename string) error {
	if filename == "" || filename == "." || filename == ".." {
		return fmt.Errorf("ファイル名が無効です")
	}
	if !utf8.ValidString(filename) || len([]byte(filename)) > maxImageFilenameBytes {
		return fmt.Errorf("ファイル名はUTF-8で%dバイト以内にしてください", maxImageFilenameBytes)
	}
	if strings.ContainsAny(filename, "/\\") || filepath.Base(filename) != filename {
		return fmt.Errorf("ファイル名にパスを含めることはできません")
	}
	if strings.ContainsFunc(filename, unicode.IsControl) {
		return fmt.Errorf("ファイル名に制御文字を含めることはできません")
	}
	return nil
}

func validateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("画像サイズが無効です")
	}
	if int64(width) > models.MaxImagePixels/int64(height) {
		return fmt.Errorf("画像は%dメガピクセル以下にしてください", models.MaxImagePixels/1_000_000)
	}
	return nil
}

func validateImageContainer(mimeType string, data []byte) error {
	switch mimeType {
	case "image/jpeg":
		if err := validateJPEGContainer(data); err != nil {
			return err
		}
	case "image/png":
		if err := validatePNGContainer(data); err != nil {
			return err
		}
	case "image/gif":
		if err := validateSingleFrameGIF(data); err != nil {
			return err
		}
	case "image/webp":
		if err := validateStillWebP(data); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未対応の画像形式です")
	}
	return nil
}

func validateJPEGContainer(data []byte) error {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return fmt.Errorf("JPEGコンテナが不正です")
	}

	offset := 2
	for offset < len(data) {
		markerStart := offset
		if data[offset] != 0xff {
			return fmt.Errorf("JPEGマーカーが不正です")
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return fmt.Errorf("JPEG終端がありません")
		}
		marker := data[offset]
		offset++

		switch {
		case marker == 0xd9:
			if offset != len(data) {
				return fmt.Errorf("JPEG終端の後にデータがあります")
			}
			return nil
		case marker == 0xd8 || marker == 0x00:
			return fmt.Errorf("JPEGマーカーが不正です")
		case marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7):
			continue
		}

		if offset+2 > len(data) {
			return fmt.Errorf("JPEGセグメントが破損しています")
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLength < 2 || segmentLength > len(data)-offset {
			return fmt.Errorf("JPEGセグメント長が不正です")
		}
		offset += segmentLength

		if marker != 0xda { // Start of Scan
			continue
		}

		foundNextMarker := false
		for offset < len(data) {
			if data[offset] != 0xff {
				offset++
				continue
			}
			markerStart = offset
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				return fmt.Errorf("JPEG走査データが破損しています")
			}
			scanMarker := data[offset]
			offset++
			if scanMarker == 0x00 || (scanMarker >= 0xd0 && scanMarker <= 0xd7) {
				continue
			}
			offset = markerStart
			foundNextMarker = true
			break
		}
		if !foundNextMarker {
			return fmt.Errorf("JPEG終端がありません")
		}
	}
	return fmt.Errorf("JPEG終端がありません")
}

func validatePNGContainer(data []byte) error {
	if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return fmt.Errorf("PNGコンテナが不正です")
	}

	offset := 8
	for offset+12 <= len(data) {
		chunkLength := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkEnd := int64(offset) + 12 + chunkLength
		if chunkEnd > int64(len(data)) {
			return fmt.Errorf("PNGチャンクが破損しています")
		}
		chunkType := string(data[offset+4 : offset+8])
		offset = int(chunkEnd)
		if chunkType == "IEND" {
			if chunkLength != 0 || offset != len(data) {
				return fmt.Errorf("PNG終端の後にデータがあります")
			}
			return nil
		}
	}
	return fmt.Errorf("PNG終端チャンクがありません")
}

func validateSingleFrameGIF(data []byte) error {
	if len(data) < 14 || (!bytes.Equal(data[:6], []byte("GIF87a")) && !bytes.Equal(data[:6], []byte("GIF89a"))) {
		return fmt.Errorf("GIFコンテナが不正です")
	}

	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 * (1 << ((data[10] & 0x07) + 1))
	}
	if offset > len(data) {
		return fmt.Errorf("GIFカラーテーブルが破損しています")
	}

	frames := 0
	for offset < len(data) {
		switch data[offset] {
		case 0x3b:
			offset++
			if frames != 1 || offset != len(data) {
				return fmt.Errorf("GIFは静止画1フレームのみ使用できます")
			}
			return nil
		case 0x21:
			if offset+2 > len(data) {
				return fmt.Errorf("GIF拡張ブロックが破損しています")
			}
			offset += 2
			var ok bool
			offset, ok = skipGIFSubBlocks(data, offset)
			if !ok {
				return fmt.Errorf("GIF拡張ブロックが破損しています")
			}
		case 0x2c:
			frames++
			if frames > 1 {
				return fmt.Errorf("アニメーションGIFは使用できません")
			}
			if offset+10 > len(data) {
				return fmt.Errorf("GIF画像ブロックが破損しています")
			}
			packed := data[offset+9]
			offset += 10
			if packed&0x80 != 0 {
				offset += 3 * (1 << ((packed & 0x07) + 1))
			}
			if offset >= len(data) {
				return fmt.Errorf("GIF画像データが破損しています")
			}
			offset++ // LZW minimum code size
			var ok bool
			offset, ok = skipGIFSubBlocks(data, offset)
			if !ok {
				return fmt.Errorf("GIF画像データが破損しています")
			}
		default:
			return fmt.Errorf("GIFブロックが不正です")
		}
	}
	return fmt.Errorf("GIF終端がありません")
}

func skipGIFSubBlocks(data []byte, offset int) (int, bool) {
	for offset < len(data) {
		blockLength := int(data[offset])
		offset++
		if blockLength == 0 {
			return offset, true
		}
		if blockLength > len(data)-offset {
			return 0, false
		}
		offset += blockLength
	}
	return 0, false
}

func validateStillWebP(data []byte) error {
	if len(data) < 20 || !bytes.Equal(data[:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WEBP")) {
		return fmt.Errorf("WebPコンテナが不正です")
	}
	declaredSize := int64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if declaredSize != int64(len(data)) {
		return fmt.Errorf("WebPコンテナ長が不正です")
	}

	offset := 12
	for offset+8 <= len(data) {
		chunkType := string(data[offset : offset+4])
		chunkLength := int64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkStart := int64(offset + 8)
		chunkEnd := chunkStart + chunkLength
		if chunkEnd > int64(len(data)) {
			return fmt.Errorf("WebPチャンクが破損しています")
		}
		if chunkType == "ANIM" || chunkType == "ANMF" {
			return fmt.Errorf("アニメーションWebPは使用できません")
		}
		if chunkType == "VP8X" && chunkLength >= 1 && data[chunkStart]&0x02 != 0 {
			return fmt.Errorf("アニメーションWebPは使用できません")
		}
		offset = int(chunkEnd)
		if chunkLength%2 != 0 {
			offset++
		}
	}
	if offset != len(data) {
		return fmt.Errorf("WebPチャンク境界が不正です")
	}
	return nil
}
