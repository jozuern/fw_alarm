<?php
// Die alte Oberfläche ist entfernt. classic.php bleibt nur, damit alte
// Lesezeichen und Links weiter ankommen: Es leitet auf die Oberfläche
// (index.php) um - ohne Sitzung und ohne Cookie. Bewusst 302, nicht 301
// (einen 301 merkt sich der Browser dauerhaft). common.php nur für die
// Sicherheits-Header (u.a. Cache-Control: no-store).
require __DIR__ . '/lib/common.php';

header('Location: index.php', true, 302);
exit;
