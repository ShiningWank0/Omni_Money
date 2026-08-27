package core

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestPrepareTransactionImageAcceptsSupportedStaticFormats(t *testing.T) {
	webpData, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		filename string
		mimeType string
		data     []byte
	}{
		{name: "JPEG", filename: "receipt.jpg", mimeType: "image/jpeg", data: encodeJPEG(t)},
		{name: "PNG with inferred MIME", filename: "receipt.png", data: encodePNG(t)},
		{name: "GIF", filename: "receipt.gif", mimeType: "image/gif", data: encodeGIF(t, 1)},
		{name: "WebP", filename: "receipt.webp", mimeType: "image/webp", data: webpData},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := prepareTransactionImage(models.TransactionImageRequest{
				Filename: tt.filename,
				MimeType: tt.mimeType,
				Data:     base64.StdEncoding.EncodeToString(tt.data),
			})
			if err != nil {
				t.Fatalf("prepareTransactionImage() error = %v", err)
			}
			if len(prepared.data) != len(tt.data) || prepared.mimeType == "" {
				t.Fatalf("prepared image = %#v", prepared)
			}
		})
	}
}

func TestPrepareTransactionImageRejectsInvalidContent(t *testing.T) {
	validPNG := encodePNG(t)
	validJPEG := encodeJPEG(t)
	animatedGIF := encodeGIF(t, 2)
	pixelBomb := pngWithDimensions(t, 5000, 5000)

	tests := []struct {
		name string
		req  models.TransactionImageRequest
	}{
		{
			name: "unsafe filename",
			req:  imageRequest("../receipt.png", "image/png", validPNG),
		},
		{
			name: "MIME mismatch",
			req:  imageRequest("receipt.png", "image/jpeg", validPNG),
		},
		{
			name: "magic mismatch",
			req:  imageRequest("receipt.png", "image/png", []byte("not an image")),
		},
		{
			name: "non canonical base64 whitespace",
			req: models.TransactionImageRequest{
				Filename: "receipt.png",
				MimeType: "image/png",
				Data:     base64.StdEncoding.EncodeToString(validPNG[:10]) + "\n" + base64.StdEncoding.EncodeToString(validPNG[10:]),
			},
		},
		{
			name: "trailing PNG payload",
			req:  imageRequest("receipt.png", "image/png", append(append([]byte{}, validPNG...), []byte("payload")...)),
		},
		{
			name: "trailing JPEG payload",
			req:  imageRequest("receipt.jpg", "image/jpeg", append(append(append([]byte{}, validJPEG...), []byte("payload")...), 0xff, 0xd9)),
		},
		{
			name: "animated GIF",
			req:  imageRequest("receipt.gif", "image/gif", animatedGIF),
		},
		{
			name: "pixel bomb header",
			req:  imageRequest("receipt.png", "image/png", pixelBomb),
		},
		{
			name: "oversized encoded data rejected before decode",
			req: models.TransactionImageRequest{
				Filename: "receipt.png",
				MimeType: "image/png",
				Data:     strings.Repeat("A", base64.StdEncoding.EncodedLen(int(models.MaxImageBytes))+4),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := prepareTransactionImage(tt.req); err == nil {
				t.Fatal("prepareTransactionImage() unexpectedly accepted invalid image")
			}
		})
	}
}

func TestPrepareTransactionImageRejectsVulnerableWebPShapesWithoutPanic(t *testing.T) {
	validWebP, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "oversized VP8X canvas", data: oversizedWebPCanvas()},
		{name: "VP8X payload dimension mismatch", data: mismatchedWebPCanvas(validWebP)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("malformed WebP caused panic: %v", recovered)
				}
			}()
			if _, err := prepareTransactionImage(imageRequest("malformed.webp", "image/webp", tt.data)); err == nil {
				t.Fatal("malformed WebP was accepted")
			}
		})
	}
}

func oversizedWebPCanvas() []byte {
	return []byte{
		'R', 'I', 'F', 'F',
		22, 0, 0, 0,
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X',
		10, 0, 0, 0,
		1 << 4, 0, 0, 0,
		0xff, 0xff, 0x00,
		0xff, 0x7f, 0x00,
	}
}

func mismatchedWebPCanvas(validWebP []byte) []byte {
	if len(validWebP) < 12 {
		return nil
	}
	vp8xChunk := []byte{
		'V', 'P', '8', 'X',
		10, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0,
		0, 0, 0,
	}
	data := append([]byte("RIFF\x00\x00\x00\x00WEBP"), vp8xChunk...)
	data = append(data, validWebP[12:]...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}

func TestPrepareTransactionImagesEnforcesBatchCount(t *testing.T) {
	request := imageRequest("receipt.png", "image/png", encodePNG(t))
	requests := make([]models.TransactionImageRequest, models.MaxImagesPerTransaction+1)
	for i := range requests {
		requests[i] = request
	}
	if _, err := prepareTransactionImages(requests); err == nil {
		t.Fatal("prepareTransactionImages() accepted too many images")
	}
}

func TestAddTransactionRollsBackWhenAnyImageIsInvalid(t *testing.T) {
	initImageTestDB(t)
	req := transactionRequest("cash", "2026-01-01", "receipt", "expense", 100)
	req.Images = []models.TransactionImageRequest{
		imageRequest("receipt.png", "image/png", encodePNG(t)),
		imageRequest("fake.png", "image/png", []byte("not an image")),
	}

	if _, err := AddTransaction(req); err == nil {
		t.Fatal("AddTransaction() accepted an invalid image")
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("transaction count = %d, want 0", count)
	}
}

func TestImageQuotaIsEnforcedByCoreAndDatabaseTrigger(t *testing.T) {
	db := initImageTestDB(t)
	result, err := db.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, 0, '')",
		"cash", "2026-01-01", "receipt", "expense", 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := prepareTransactionImage(imageRequest("receipt.png", "image/png", encodePNG(t)))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	images := make([]preparedTransactionImage, models.MaxImagesPerTransaction)
	for i := range images {
		images[i] = prepared
	}
	if err := insertPreparedTransactionImages(tx, transactionID, images); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := AddTransactionImage(transactionID, imageRequest("extra.png", "image/png", encodePNG(t))); err == nil {
		t.Fatal("AddTransactionImage() exceeded the per-transaction image count")
	}
	if _, err := db.Exec(
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, 'bypass.png', ?, 'image/png')",
		transactionID, prepared.data,
	); err == nil {
		t.Fatal("database trigger allowed an upload to bypass the quota")
	}

	usage, err := GetImageStorageUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.ImageCount != int64(models.MaxImagesPerTransaction) || len(usage.Accounts) != 1 {
		t.Fatalf("usage = %#v", usage)
	}
	if usage.Bytes != int64(len(prepared.data))*int64(models.MaxImagesPerTransaction) {
		t.Fatalf("usage bytes = %d", usage.Bytes)
	}
}

func TestInvalidLegacyImageIsNeverReturnedAsDataURL(t *testing.T) {
	db := initImageTestDB(t)
	first, err := db.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES ('cash', '2026-01-01', 'first', 'expense', 100, 0, '')",
	)
	if err != nil {
		t.Fatal(err)
	}
	transactionID, _ := first.LastInsertId()
	second, err := db.Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES ('cash', '2026-01-02', 'second', 'expense', 100, 0, '')",
	)
	if err != nil {
		t.Fatal(err)
	}
	otherTransactionID, _ := second.LastInsertId()
	imageResult, err := db.Exec(
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, 'legacy.png', ?, 'image/png')",
		transactionID, []byte("legacy non-image data"),
	)
	if err != nil {
		t.Fatal(err)
	}
	imageID, _ := imageResult.LastInsertId()

	images, err := GetTransactionImages(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || !images[0].Invalid || images[0].DataURL != "" {
		t.Fatalf("legacy image response = %#v", images)
	}
	if err := DeleteTransactionImageForTransaction(otherTransactionID, imageID); err == nil {
		t.Fatal("delete accepted an image belonging to another transaction")
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM transaction_images WHERE id = ?", imageID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("remaining image count = %d, want 1", remaining)
	}
}

func imageRequest(filename, mimeType string, data []byte) models.TransactionImageRequest {
	return models.TransactionImageRequest{
		Filename: filename,
		MimeType: mimeType,
		Data:     base64.StdEncoding.EncodeToString(data),
	}
}

func encodePNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func encodeJPEG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewNRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func encodeGIF(t *testing.T, frames int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	animation := &gif.GIF{}
	for i := 0; i < frames; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
		frame.Pix[0] = uint8(i % 2)
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 1)
	}
	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, animation); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func pngWithDimensions(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8
	ihdr[9] = 2
	writePNGChunk(&buffer, "IHDR", ihdr)
	writePNGChunk(&buffer, "IEND", nil)
	return buffer.Bytes()
}

func writePNGChunk(buffer *bytes.Buffer, chunkType string, data []byte) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(data)))
	buffer.WriteString(chunkType)
	buffer.Write(data)
	checksumInput := append([]byte(chunkType), data...)
	_ = binary.Write(buffer, binary.BigEndian, crc32.ChecksumIEEE(checksumInput))
}

func initImageTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "omni_money_test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	return database.GetDB()
}
