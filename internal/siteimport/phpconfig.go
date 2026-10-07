package siteimport

// PHP configuration both the import and the project templates add to a CMS's own
// configuration. Everything reads the variables Envoryx injects at runtime, so it follows
// a rename, a duplicate, an added or removed service without rewriting the file.

// WordPressAddress keeps WordPress on the address it is opened with. WordPress stores its
// own URL in the database, so a copied database (an import, a duplicate, a renamed
// project, an extra domain, a share) would send every link and the wp-admin login to the
// old address. Without a request (cron, a script) the project URL stands in.
const WordPressAddress = `// Added by Envoryx: the site answers on the address it is opened with, and the proxy
// in front of it terminates HTTPS.
if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') {
    $_SERVER['HTTPS'] = 'on';
}
if (!empty($_SERVER['HTTP_HOST'])) {
    define('WP_HOME', (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off' ? 'https' : 'http') . '://' . $_SERVER['HTTP_HOST']);
    define('WP_SITEURL', WP_HOME);
} elseif (getenv('ENVORYX_URL')) {
    define('WP_HOME', getenv('ENVORYX_URL'));
    define('WP_SITEURL', WP_HOME);
}
`

// WordPressRedis points the Redis object cache plugins at the project's Redis; they
// default to 127.0.0.1, where nothing listens. Without Redis in the project it does
// nothing.
const WordPressRedis = `// Added by Envoryx: the project's Redis for an object cache plugin (Envoryx injects REDIS_*).
if (getenv('REDIS_HOST')) {
    define('WP_REDIS_HOST', getenv('REDIS_HOST'));
    define('WP_REDIS_PORT', (int) (getenv('REDIS_PORT') ?: 6379));
    if (getenv('REDIS_PASSWORD')) {
        define('WP_REDIS_PASSWORD', getenv('REDIS_PASSWORD'));
    }
}
`

// DrupalTrustedHosts lets Drupal check the Host header (its status report flags a site
// without trusted_host_patterns as an error) without breaking any address the project is
// opened with: the project URL and every name under the same base domain (extra domains
// there, the dev host), a share, and the published port on this machine. It replaces
// the patterns of an imported site, which name the old server.
const DrupalTrustedHosts = `// Added by Envoryx: the host names the site answers to. A domain of your own outside
// the project's base domain needs a pattern of its own below this block.
$settings['trusted_host_patterns'] = ['^localhost$', '^127\.0\.0\.1$', '^[a-z0-9-]+\.trycloudflare\.com$'];
$envoryx_host = (string) parse_url((string) getenv('ENVORYX_URL'), PHP_URL_HOST);
if ($envoryx_host !== '') {
  $settings['trusted_host_patterns'][] = '^' . preg_quote($envoryx_host) . '$';
  $envoryx_slug = (string) getenv('ENVORYX_PROJECT');
  if ($envoryx_slug !== '' && str_starts_with($envoryx_host, $envoryx_slug . '.')) {
    $settings['trusted_host_patterns'][] = '\.' . preg_quote(substr($envoryx_host, strlen($envoryx_slug) + 1)) . '$';
  }
}
unset($envoryx_host, $envoryx_slug);
`

// DrushYML gives Drush the site's address, which it cannot know without a request:
// without it "drush uli" prints http://default/... links.
const DrushYML = `# Added by Envoryx: the address Drush uses for the links it prints (drush uli),
# the project URL Envoryx injects.
options:
  uri: '${env.ENVORYX_URL}'
`

// TYPO3ProxyAndMail tells TYPO3 that the proxy in front ends HTTPS (TYPO3 only believes
// X-Forwarded-Proto from a proxy it knows; a request to the published port without the
// header stays plain HTTP), gives it a sender address (its mail test refuses to send
// without one) and, with Mailpit in the project, sends mail there over SMTP. The
// installer otherwise copies the msmtp command from php.ini into settings.php, which
// outlives a removed Mailpit.
const TYPO3ProxyAndMail = `// Added by Envoryx: the proxy in front of the site terminates HTTPS.
if (($_SERVER['HTTP_X_FORWARDED_PROTO'] ?? '') === 'https' && !empty($_SERVER['REMOTE_ADDR'])) {
    $GLOBALS['TYPO3_CONF_VARS']['SYS']['reverseProxyIP'] = $_SERVER['REMOTE_ADDR'];
    $GLOBALS['TYPO3_CONF_VARS']['SYS']['reverseProxySSL'] = $_SERVER['REMOTE_ADDR'];
}
// Added by Envoryx: a sender address, and Mailpit when the project has it (Envoryx injects MAIL_*).
if (empty($GLOBALS['TYPO3_CONF_VARS']['MAIL']['defaultMailFromAddress'])) {
    $GLOBALS['TYPO3_CONF_VARS']['MAIL']['defaultMailFromAddress'] = 'noreply@' . (parse_url((string) getenv('ENVORYX_URL'), PHP_URL_HOST) ?: 'example.com');
}
if (getenv('MAIL_HOST')) {
    $GLOBALS['TYPO3_CONF_VARS']['MAIL']['transport'] = 'smtp';
    $GLOBALS['TYPO3_CONF_VARS']['MAIL']['transport_smtp_server'] = getenv('MAIL_HOST') . ':' . (getenv('MAIL_PORT') ?: '25');
}
`
