package model

// maxBoxVersion: größte Listen-Version der Box, die zählt (2^31 − 1). Die
// Firmware zählt ihre Versionen klein hoch; was darüber liegt, ist Unsinn
// und höbe die Version des Servers ins Uferlose – bei der größten Zahl über
// den Rand von int hinaus ins Negative.
const maxBoxVersion = 1<<31 - 1

// BoxKeysVersion: die Listen-Version, die die Box mit ihrem letzten Status
// gemeldet hat – so zählt sie bei jeder Erhöhung mit (Bump,
// ExpireStaleMutes; PHP: (int)(latest_status.json['keys_version'] ?? 0) in
// bump_keys_version). box nil (nie gemeldet), ein fehlendes Feld und eine
// unplausible Zahl (negativ oder ab 2^31) zählen als 0.
func BoxKeysVersion(box *BoxStatus) int {
	if box == nil {
		return 0
	}
	v := phpInt(box.Fields.Get("keys_version"))
	if v < 0 || v > maxBoxVersion {
		return 0
	}
	return int(v)
}
