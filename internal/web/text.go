package web

import "fwalarm/internal/model"

// ClipBytes kürzt s auf höchstens n Bytes, nie mitten in einem Zeichen
// (ein ungültiges Byte zählt als eigenes Zeichen). Die Regel steht einmal,
// in model.ClipBytes – der Status-Push der Box kürzt dort die device_id.
func ClipBytes(s string, n int) string { return model.ClipBytes(s, n) }
