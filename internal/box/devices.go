package box

import (
	"fwalarm/internal/dashapi"
	"fwalarm/internal/model"
)

// Der Teil device_hint der Antwort von status (PHP: device_hint($config) in
// dashboard_api.php, für jede Rolle): der Hinweis auf mehrere bzw. fremde
// Boards mit dem Maschinen-Token, aus der Liste der Boards, die der
// Status-Push führt (model.EvaluatePush), mit box_device_id und der Schwelle
// offline_after_seconds aus der Konfiguration.
func init() {
	dashapi.StatusPart("device_hint", func(x *dashapi.Ctx) any {
		cfg := x.Core.Cfg()
		return model.DeviceHint(x.Core.Store.Runtime(), cfg.BoxDeviceID, x.Core.Clock.Now(), cfg.OfflineAfterSeconds)
	})
}
