<?php
// Schreibt <dir>/site/config.php für die lokale Test-Sandbox.
// Aufruf: php make-config.php <dir>
$dir = $argv[1] ?? '';
if ($dir === '') {
    fwrite(STDERR, "Aufruf: php make-config.php <dir>\n");
    exit(2);
}
$dir = rtrim(str_replace('\\', '/', $dir), '/');
$config = require __DIR__ . '/../../../nas_dashboard/config.example.php';

// data_dir() lehnt "C:\..." ab: Laufwerksbuchstabe weglassen (/Users/...).
$data = preg_replace('~^[A-Za-z]:~', '', $dir) . '/data';

$config['machine_token_sha256'] = hash('sha256', 'box-token');
$config['dashboard_password_hash'] = password_hash('test', PASSWORD_DEFAULT);
$config['dashboard_readonly_user'] = 'leser';
$config['dashboard_readonly_password_hash'] = password_hash('test', PASSWORD_DEFAULT);
$config['bark_key_status'] = 'statuskey0001';
$config['bark_host'] = 'http://127.0.0.1:8789';
$config['bark_keys_alarm'] = ['startkey0001'];
$config['data_dir'] = $data;
$config['health_check_url'] = 'http://127.0.0.1:8787/api/health.php';
$config['offline_after_seconds'] = 30;

$out = "<?php\nreturn " . var_export($config, true) . ";\n";
if (file_put_contents($dir . '/site/config.php', $out) === false) {
    fwrite(STDERR, "config.php nicht schreibbar\n");
    exit(1);
}
echo "config.php geschrieben, data_dir=$data\n";
