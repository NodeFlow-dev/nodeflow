// Russian hover documentation for haproxy.cfg. Pure data: no runtime imports,
// so node --test can load it with built-in type stripping.
//
// Keys are lowercase. Compound directives ("timeout connect", "option tcplog")
// are keyed by their full phrase. Argument docs are grouped by the directive
// that owns them (server options, bind options, balance algorithms…).

export type SectionKind = 'global' | 'defaults' | 'frontend' | 'backend' | 'listen' | 'resolvers' | 'peers' | 'userlist' | 'cache' | 'program' | 'ring' | 'http-errors' | 'crt-store' | 'mailers' | 'log-forward';

export interface HAProxyDoc {
  /** 1–2 sentences, Russian. */
  summary: string;
  /** One syntax line, rendered as a haproxy code block. */
  syntax?: string;
  /** Markdown warning line, «⚠ Если **…**, то …». */
  warning?: string;
  /** Sections where a directive is valid (used for completion). */
  sections?: SectionKind[];
}

const PROXY: SectionKind[] = ['defaults', 'frontend', 'backend', 'listen'];
const FE: SectionKind[] = ['defaults', 'frontend', 'listen'];
const BE: SectionKind[] = ['defaults', 'backend', 'listen'];
const BE_ONLY: SectionKind[] = ['backend', 'listen'];
const GLOBAL: SectionKind[] = ['global'];
const RESOLVERS: SectionKind[] = ['resolvers'];

export const SECTION_KEYWORDS: readonly string[] = ['global', 'defaults', 'frontend', 'backend', 'listen', 'resolvers', 'peers', 'userlist', 'cache', 'program', 'ring', 'http-errors', 'crt-store', 'mailers', 'log-forward'];

export const GENERATED_NAME_DOC: HAProxyDoc = {
  summary: 'Имя сгенерировано NodeFlow; на него завязаны метрики и квоты. **Не переименовывать.**',
};

export const sectionDocs: Record<string, HAProxyDoc> = {
  global: { summary: 'Параметры процесса HAProxy: лимиты, потоки, логи, stats-сокет, пользователь. Секция одна на весь конфиг.', syntax: 'global' },
  defaults: { summary: 'Значения по умолчанию для всех следующих frontend/backend/listen: режим, таймауты, опции логирования.', syntax: 'defaults [<имя>]', warning: '⚠ Если добавить **второй defaults**, то он применяется только к секциям ниже него.' },
  frontend: { summary: 'Точка входа: слушает порты (bind), принимает соединения и выбирает backend по правилам.', syntax: 'frontend <имя>' },
  backend: { summary: 'Группа серверов назначения, алгоритм балансировки и проверки здоровья.', syntax: 'backend <имя>' },
  listen: { summary: 'Frontend и backend в одной секции: bind и server рядом. Удобно для простых TCP-прокси.', syntax: 'listen <имя>' },
  resolvers: { summary: 'Набор DNS-серверов, через которые HAProxy перерезолвит имена серверов во время работы.', syntax: 'resolvers <имя>' },
  peers: { summary: 'Синхронизация stick-table между процессами HAProxy (в NodeFlow — между старым и новым процессом при reload).', syntax: 'peers <имя>' },
  userlist: { summary: 'Список пользователей и групп для HTTP basic-auth.', syntax: 'userlist <имя>' },
  cache: { summary: 'Кеш HTTP-ответов в памяти. Для TCP-маршрутов не используется.', syntax: 'cache <имя>' },
  program: { summary: 'Внешняя программа, которую запускает и перезапускает master-процесс HAProxy.', syntax: 'program <имя>' },
  ring: { summary: 'Кольцевой буфер в памяти для логов и трассировок.', syntax: 'ring <имя>' },
  'http-errors': { summary: 'Набор пользовательских страниц ошибок (errorfile) для HTTP-режима.', syntax: 'http-errors <имя>' },
  'crt-store': { summary: 'Хранилище сертификатов и ключей, на которые ссылаются bind-строки.', syntax: 'crt-store [<имя>]' },
  mailers: { summary: 'SMTP-серверы для почтовых уведомлений об изменении состояния серверов.', syntax: 'mailers <имя>' },
  'log-forward': { summary: 'Приём и пересылка syslog-сообщений через HAProxy.', syntax: 'log-forward <имя>' },
};

export const directiveDocs: Record<string, HAProxyDoc> = {
  // ---- conditional blocks
  '.if': { summary: 'Условный блок конфигурации: строки до .endif читаются, только если условие истинно (проверяется при загрузке конфига).', syntax: '.if <условие> … [.elif …] [.else] .endif', warning: '⚠ Если не закрыть блок **.endif**, то HAProxy не загрузит конфиг.' },
  '.elif': { summary: 'Альтернативная ветка условного блока .if.', syntax: '.elif <условие>' },
  '.else': { summary: 'Ветка «иначе» условного блока .if.', syntax: '.else' },
  '.endif': { summary: 'Закрывает условный блок .if.', syntax: '.endif' },
  '.notice': { summary: 'Выводит сообщение уровня notice при загрузке конфига.', syntax: '.notice "<текст>"' },
  '.warning': { summary: 'Выводит предупреждение при загрузке конфига.', syntax: '.warning "<текст>"' },
  '.alert': { summary: 'Выводит ошибку и прерывает загрузку конфига.', syntax: '.alert "<текст>"' },
  '.diag': { summary: 'Выводит диагностическое сообщение (только с ключом -dD).', syntax: '.diag "<текст>"' },

  // ---- global
  maxconn: { summary: 'Максимум одновременных соединений: в global — на весь процесс, во frontend — на этот вход. Лишние соединения ждут в очереди ядра.', syntax: 'maxconn <число>', warning: '⚠ Если поставить **слишком мало**, то новые клиенты будут ждать или отваливаться по таймауту.', sections: ['global', 'defaults', 'frontend', 'listen'] },
  nbthread: { summary: 'Число рабочих потоков HAProxy. По умолчанию — по числу доступных CPU.', syntax: 'nbthread <число>', warning: '⚠ Если указать **больше ядер**, чем есть, то потоки конкурируют и задержка растёт.', sections: GLOBAL },
  log: { summary: 'Куда отправлять логи: syslog-адрес или сокет, facility и уровень. «log global» в прокси берёт настройки из global.', syntax: 'log <адрес> [len <n>] <facility> [<уровень>] | log global', warning: '⚠ Если писать лог **каждого соединения** на маленький диск, то syslog быстро заполнит место.', sections: ['global', 'defaults', 'frontend', 'backend', 'listen'] },
  'log global': { summary: 'Использовать те же log-цели, что заданы в секции global.', syntax: 'log global', sections: PROXY },
  'log-format': { summary: 'Шаблон строки лога соединения с переменными вида %ci, %ft, %B.', syntax: 'log-format "<формат>"', sections: PROXY },
  'stats socket': { summary: 'UNIX-сокет Runtime API: статистика, включение/выключение серверов, веса, таблицы. NodeFlow Agent читает через него метрики и управляет квотами.', syntax: 'stats socket <путь> [mode <права>] [level <уровень>] [expose-fd listeners]', warning: '⚠ Если **удалить или переименовать** /run/haproxy/admin.sock, то метрики, квоты и мягкий reload в NodeFlow перестанут работать.', sections: GLOBAL },
  'stats timeout': { summary: 'Сколько ждать команду на stats-сокете, прежде чем закрыть соединение.', syntax: 'stats timeout <время>', sections: GLOBAL },
  user: { summary: 'Пользователь, от имени которого работает HAProxy после старта.', syntax: 'user <имя>', sections: GLOBAL },
  group: { summary: 'Группа, от имени которой работает HAProxy после старта.', syntax: 'group <имя>', sections: GLOBAL },
  pidfile: { summary: 'Файл, в который записывается PID процесса.', syntax: 'pidfile <путь>', sections: GLOBAL },
  daemon: { summary: 'Запуск в фоне. Под systemd обычно не нужен: сервис использует master-worker режим.', syntax: 'daemon', sections: GLOBAL },
  'master-worker': { summary: 'Режим master-worker: мастер-процесс перезапускает воркеры при reload без потери сокетов.', syntax: 'master-worker', sections: GLOBAL },
  chroot: { summary: 'Переводит процесс в chroot-каталог после старта.', syntax: 'chroot <каталог>', warning: '⚠ Если включить **chroot**, то пути к файлам (acl -f, errorfile) должны быть доступны внутри каталога.', sections: GLOBAL },
  'hard-stop-after': { summary: 'Сколько старый процесс может дожидаться завершения соединений после reload, прежде чем закрыть их принудительно.', syntax: 'hard-stop-after <время>', warning: '⚠ Если значение **не задано**, то старые процессы с долгими TCP-сессиями живут неограниченно и держат память.', sections: GLOBAL },
  'ssl-default-bind-ciphers': { summary: 'Список шифров TLS ≤1.2 по умолчанию для bind с ssl.', syntax: 'ssl-default-bind-ciphers <список>', sections: GLOBAL },
  'ssl-default-bind-ciphersuites': { summary: 'Список шифров TLS 1.3 по умолчанию для bind с ssl.', syntax: 'ssl-default-bind-ciphersuites <список>', sections: GLOBAL },
  'ssl-default-bind-options': { summary: 'Опции TLS по умолчанию для bind: минимальная версия, отключение tickets и т.п.', syntax: 'ssl-default-bind-options ssl-min-ver TLSv1.2 …', sections: GLOBAL },
  'ssl-default-server-ciphers': { summary: 'Список шифров по умолчанию для TLS-соединений к серверам.', syntax: 'ssl-default-server-ciphers <список>', sections: GLOBAL },
  'ssl-default-server-options': { summary: 'Опции TLS по умолчанию для соединений к серверам.', syntax: 'ssl-default-server-options <опции>', sections: GLOBAL },
  'tune.pipesize': { summary: 'Размер pipe-буфера ядра для splice. NodeFlow подбирает его по объёму памяти ноды.', syntax: 'tune.pipesize <байт>', warning: '⚠ Если задать **слишком большой** размер на ноде с малой памятью, то под нагрузкой не хватит RAM.', sections: GLOBAL },
  'tune.bufsize': { summary: 'Размер буфера на соединение. Влияет на память и на максимальный размер заголовков.', syntax: 'tune.bufsize <байт>', warning: '⚠ Если **увеличить** bufsize, то память на maxconn соединений растёт пропорционально.', sections: GLOBAL },
  'tune.maxrewrite': { summary: 'Резерв буфера под перезапись HTTP-заголовков.', syntax: 'tune.maxrewrite <байт>', sections: GLOBAL },
  'tune.ssl.default-dh-param': { summary: 'Размер DH-параметров для TLS.', syntax: 'tune.ssl.default-dh-param <бит>', sections: GLOBAL },
  localpeer: { summary: 'Имя этого процесса в секции peers. NodeFlow использует его для переноса stick-table между процессами при reload.', syntax: 'localpeer <имя>', sections: GLOBAL },
  noreuseport: { summary: 'Отключает SO_REUSEPORT на слушающих сокетах.', syntax: 'noreuseport', sections: GLOBAL },
  'cpu-map': { summary: 'Привязка потоков HAProxy к ядрам CPU.', syntax: 'cpu-map [auto:]<поток> <cpu>', sections: GLOBAL },
  'ulimit-n': { summary: 'Лимит открытых файлов процесса. По умолчанию вычисляется из maxconn.', syntax: 'ulimit-n <число>', sections: GLOBAL },
  'expose-experimental-directives': { summary: 'Разрешает экспериментальные директивы.', syntax: 'expose-experimental-directives', sections: GLOBAL },

  // ---- defaults / proxies
  mode: { summary: 'Режим прокси: tcp — прозрачная пересылка байтов (NodeFlow), http — разбор HTTP.', syntax: 'mode tcp|http', warning: '⚠ Если frontend и backend в **разных режимах**, то HAProxy откажется загружать конфиг.', sections: PROXY },
  'timeout connect': { summary: 'Сколько ждать установления TCP-соединения с сервером.', syntax: 'timeout connect <время>', warning: '⚠ Если значение **слишком большое**, то клиент долго ждёт при недоступном сервере вместо перехода на другой.', sections: PROXY },
  'timeout client': { summary: 'Максимальная пауза без данных со стороны клиента. После неё соединение закрывается.', syntax: 'timeout client <время>', warning: '⚠ Если поставить **слишком мало** для VPN/прокси-трафика, то простаивающие сессии будут рваться.', sections: FE },
  'timeout server': { summary: 'Максимальная пауза без данных со стороны сервера.', syntax: 'timeout server <время>', warning: '⚠ Если значение **меньше timeout client**, то длинные сессии обрываются со стороны сервера.', sections: BE },
  'timeout tunnel': { summary: 'Таймаут бездействия для установившегося туннеля (WebSocket, CONNECT, TCP). Заменяет client/server после установления.', syntax: 'timeout tunnel <время>', sections: BE },
  'timeout check': { summary: 'Дополнительный таймаут на чтение ответа health-check после соединения.', syntax: 'timeout check <время>', sections: BE },
  'timeout queue': { summary: 'Сколько соединение может ждать в очереди, когда у серверов исчерпан maxconn.', syntax: 'timeout queue <время>', sections: BE },
  'timeout client-fin': { summary: 'Таймаут бездействия клиента после полузакрытия соединения (FIN).', syntax: 'timeout client-fin <время>', sections: FE },
  'timeout server-fin': { summary: 'Таймаут бездействия сервера после полузакрытия соединения (FIN).', syntax: 'timeout server-fin <время>', sections: BE },
  'timeout http-request': { summary: 'Время на получение полного HTTP-запроса (защита от slowloris).', syntax: 'timeout http-request <время>', sections: FE },
  'timeout http-keep-alive': { summary: 'Сколько ждать следующий HTTP-запрос в keep-alive соединении.', syntax: 'timeout http-keep-alive <время>', sections: PROXY },
  'timeout resolve': { summary: 'Интервал периодического перерезолва имён в секции resolvers.', syntax: 'timeout resolve <время>', sections: RESOLVERS },
  'timeout retry': { summary: 'Сколько ждать ответа DNS-сервера перед повторным запросом.', syntax: 'timeout retry <время>', sections: RESOLVERS },
  'option tcplog': { summary: 'Расширенный формат лога для TCP: адреса, время, байты, состояние завершения.', syntax: 'option tcplog', sections: PROXY },
  'option httplog': { summary: 'Расширенный формат лога для HTTP.', syntax: 'option httplog', sections: PROXY },
  'option dontlognull': { summary: 'Не логировать соединения без данных (сканеры портов, проверки балансировщиков).', syntax: 'option dontlognull', sections: FE },
  'option splice-request': { summary: 'Пересылка данных клиент→сервер через splice ядра без копирования в user space. Экономит CPU.', syntax: 'option splice-request', sections: PROXY },
  'option splice-response': { summary: 'Пересылка данных сервер→клиент через splice ядра без копирования в user space.', syntax: 'option splice-response', sections: PROXY },
  'option splice-auto': { summary: 'HAProxy сам решает, когда использовать splice.', syntax: 'option splice-auto', sections: PROXY },
  'option redispatch': { summary: 'При неудачном подключении разрешает повтор на другой сервер, даже если клиент «прилип» к исходному.', syntax: 'option redispatch [<интервал>]', sections: BE },
  'option tcp-check': { summary: 'Health-check через TCP-сценарий (tcp-check connect/send/expect). Без правил — просто проверка соединения.', syntax: 'option tcp-check', sections: BE },
  'option httpchk': { summary: 'Health-check HTTP-запросом; сервер считается живым при ответе 2xx/3xx.', syntax: 'option httpchk [<метод> <uri>]', sections: BE },
  'option allbackups': { summary: 'Когда все основные серверы недоступны — балансировать между всеми backup-серверами, а не только первым.', syntax: 'option allbackups', sections: BE },
  'option clitcpka': { summary: 'Включить TCP keepalive к клиенту: ядро будет замечать «мёртвые» клиентские соединения.', syntax: 'option clitcpka', sections: FE },
  'option srvtcpka': { summary: 'Включить TCP keepalive к серверу.', syntax: 'option srvtcpka', sections: BE },
  'option tcpka': { summary: 'TCP keepalive в обе стороны.', syntax: 'option tcpka', sections: PROXY },
  'option forwardfor': { summary: 'Добавлять заголовок X-Forwarded-For (только HTTP-режим).', syntax: 'option forwardfor', sections: PROXY },
  'option abortonclose': { summary: 'Снимать запрос из очереди, если клиент уже закрыл соединение.', syntax: 'option abortonclose', sections: BE },
  'option log-health-checks': { summary: 'Логировать смену результатов health-check.', syntax: 'option log-health-checks', sections: BE },
  'no option': { summary: 'Отключает опцию, унаследованную из defaults.', syntax: 'no option <опция>', sections: PROXY },
  'clitcpka-idle': { summary: 'Через сколько простоя ядро начинает слать keepalive-пробы клиенту.', syntax: 'clitcpka-idle <время>', sections: FE },
  'clitcpka-intvl': { summary: 'Интервал между keepalive-пробами клиенту.', syntax: 'clitcpka-intvl <время>', sections: FE },
  'clitcpka-cnt': { summary: 'Сколько неотвеченных keepalive-проб до закрытия клиентского соединения.', syntax: 'clitcpka-cnt <число>', sections: FE },
  'srvtcpka-idle': { summary: 'Через сколько простоя ядро начинает слать keepalive-пробы серверу.', syntax: 'srvtcpka-idle <время>', sections: BE },
  'srvtcpka-intvl': { summary: 'Интервал между keepalive-пробами серверу.', syntax: 'srvtcpka-intvl <время>', sections: BE },
  'srvtcpka-cnt': { summary: 'Сколько неотвеченных keepalive-проб до закрытия серверного соединения.', syntax: 'srvtcpka-cnt <число>', sections: BE },
  retries: { summary: 'Сколько раз повторять неудачное подключение к серверу.', syntax: 'retries <число>', sections: BE },
  balance: { summary: 'Алгоритм выбора сервера в backend.', syntax: 'balance roundrobin|static-rr|leastconn|first|source|random[(<n>)]|hash <выражение>', warning: '⚠ Если сменить алгоритм **с source/hash**, то клиенты потеряют привязку к серверам.', sections: BE },
  'hash-type': { summary: 'Как хеш (balance source/hash) отображается на серверы: map-based — быстро, consistent — минимальные перестановки при изменениях.', syntax: 'hash-type map-based|consistent [<функция>] [<модификатор>]', sections: BE },
  'hash-balance-factor': { summary: 'Для consistent-хеша: сервер не получит больше указанного процента от средней нагрузки.', syntax: 'hash-balance-factor <процент>', sections: BE },
  bind: { summary: 'Адрес и порт, на которых слушает frontend. Можно указать несколько bind.', syntax: 'bind [<адрес>]:<порт>[,…] [опции]', warning: '⚠ Если порт **уже занят** другим процессом, то HAProxy не запустится после reload.', sections: ['frontend', 'listen', 'peers', 'log-forward'] },
  'tcp-request inspect-delay': { summary: 'Сколько ждать данные клиента для проверки content-правил (например, SNI из TLS ClientHello).', syntax: 'tcp-request inspect-delay <время>', warning: '⚠ Если убрать **inspect-delay**, то SNI-правила не успеют увидеть ClientHello и трафик уйдёт в default_backend.', sections: PROXY },
  'tcp-request content': { summary: 'Правило над содержимым соединения после inspect-delay: accept, reject, установка лимитов и т.п.', syntax: 'tcp-request content <действие> [if|unless <условие>]', sections: PROXY },
  'tcp-request connection': { summary: 'Правило сразу после accept(), до чтения данных: принять, отклонить, ожидать PROXY-заголовок.', syntax: 'tcp-request connection <действие> [if|unless <условие>]', sections: FE },
  'tcp-request session': { summary: 'Правило после handshake (PROXY/TLS), до чтения данных.', syntax: 'tcp-request session <действие> [if|unless <условие>]', sections: FE },
  'tcp-response content': { summary: 'Правило над данными от сервера.', syntax: 'tcp-response content <действие> [if|unless <условие>]', sections: BE },
  'tcp-check connect': { summary: 'Шаг TCP health-check: открыть соединение.', syntax: 'tcp-check connect [port <n>] [send-proxy]', sections: BE },
  'tcp-check send': { summary: 'Шаг TCP health-check: отправить строку.', syntax: 'tcp-check send <строка>', sections: BE },
  'tcp-check expect': { summary: 'Шаг TCP health-check: ожидаемый ответ.', syntax: 'tcp-check expect string|rstring <шаблон>', sections: BE },
  use_backend: { summary: 'Отправить соединение в указанный backend, если условие истинно. Правила проверяются по порядку.', syntax: 'use_backend <backend> [if|unless <условие>]', warning: '⚠ Если backend **не существует**, то HAProxy не загрузит конфиг.', sections: FE },
  default_backend: { summary: 'Backend для соединений, не подошедших ни под одно use_backend.', syntax: 'default_backend <backend>', sections: FE },
  acl: { summary: 'Именованное условие: выборка (src, req.ssl_sni…) плюс значения для сравнения. Используется в if/unless.', syntax: 'acl <имя> <выборка> [флаги] <значения>', sections: PROXY },
  server: { summary: 'Сервер назначения в backend: имя, адрес:порт и опции проверок, веса, PROXY protocol.', syntax: 'server <имя> <адрес>[:<порт>] [опции]', warning: '⚠ Если **переименовать** сервер nf_srv_*, то NodeFlow потеряет его метрики и квоты.', sections: ['backend', 'listen', 'peers'] },
  'server-template': { summary: 'Создаёт N серверов из одного DNS-имени: каждый слот получает один из адресов записи.', syntax: 'server-template <префикс> <кол-во> <fqdn>:<порт> [опции]', sections: BE_ONLY },
  'default-server': { summary: 'Опции по умолчанию для всех server в секции.', syntax: 'default-server [опции]', sections: BE },
  'stick-table': { summary: 'Таблица в памяти для привязки клиентов к серверам или счётчиков (скорость, соединения).', syntax: 'stick-table type <тип> size <n> [expire <время>] [peers <секция>] [store <данные>]', warning: '⚠ Если **size** мал для числа клиентов, то старые записи вытесняются и привязка теряется.', sections: PROXY },
  'stick on': { summary: 'Запомнить в stick-table, на какой сервер ушёл клиент, и отправлять его туда же.', syntax: 'stick on <выражение> [table <таблица>]', sections: BE_ONLY },
  'stick match': { summary: 'Искать клиента в stick-table без записи.', syntax: 'stick match <выражение> [table <таблица>]', sections: BE_ONLY },
  'stick store-request': { summary: 'Записывать клиента в stick-table без поиска.', syntax: 'stick store-request <выражение>', sections: BE_ONLY },
  'filter bwlim-in': { summary: 'Фильтр ограничения скорости входящего (upload) трафика. Лимит применяется через tcp-request content set-bandwidth-limit.', syntax: 'filter bwlim-in <имя> limit <скорость> key <выражение> [table <таблица>] [min-size <байт>]', sections: PROXY },
  'filter bwlim-out': { summary: 'Фильтр ограничения скорости исходящего (download) трафика.', syntax: 'filter bwlim-out <имя> limit <скорость> key <выражение> [table <таблица>] [min-size <байт>]', sections: PROXY },
  filter: { summary: 'Подключает фильтр потока данных (bwlim-in/out, compression, trace…).', syntax: 'filter <тип> [параметры]', sections: PROXY },
  errorfile: { summary: 'Файл ответа для HTTP-кода ошибки.', syntax: 'errorfile <код> <файл>', sections: PROXY },
  'http-request': { summary: 'Правило обработки HTTP-запроса (только mode http).', syntax: 'http-request <действие> [if|unless <условие>]', sections: PROXY },
  'http-response': { summary: 'Правило обработки HTTP-ответа (только mode http).', syntax: 'http-response <действие> [if|unless <условие>]', sections: PROXY },
  'http-check': { summary: 'Шаг HTTP health-check.', syntax: 'http-check <действие> …', sections: BE },
  fullconn: { summary: 'Нагрузка на backend, при которой серверы используют полный maxconn (для динамического minconn).', syntax: 'fullconn <число>', sections: BE },
  backlog: { summary: 'Длина очереди ядра для ещё не принятых соединений.', syntax: 'backlog <число>', sections: FE },
  'rate-limit sessions': { summary: 'Ограничение новых сессий в секунду на frontend.', syntax: 'rate-limit sessions <число>', sections: FE },
  description: { summary: 'Текстовое описание секции (видно в статистике).', syntax: 'description <текст>', sections: PROXY },
  disabled: { summary: 'Секция загружается, но не работает.', syntax: 'disabled', sections: PROXY },
  enabled: { summary: 'Явно включает секцию (по умолчанию).', syntax: 'enabled', sections: PROXY },
  'monitor-uri': { summary: 'URI для внешних health-проверок самого HAProxy.', syntax: 'monitor-uri <uri>', sections: FE },
  source: { summary: 'Исходный адрес для соединений к серверам.', syntax: 'source <адрес>[:<порт>]', sections: BE },

  // ---- resolvers
  nameserver: { summary: 'DNS-сервер для этой секции resolvers.', syntax: 'nameserver <имя> <адрес>:<порт>', sections: RESOLVERS },
  resolve_retries: { summary: 'Сколько раз повторять DNS-запрос, прежде чем считать резолв неудачным.', syntax: 'resolve_retries <число>', sections: RESOLVERS },
  hold: { summary: 'Сколько хранить последний результат DNS для заданного статуса ответа (valid, nx, timeout…).', syntax: 'hold <статус> <время>', warning: '⚠ Если **hold valid** слишком велик, то смена IP у бэкенда подхватится с задержкой.', sections: RESOLVERS },
  'parse-resolv-conf': { summary: 'Взять nameserver-ы из /etc/resolv.conf.', syntax: 'parse-resolv-conf', sections: RESOLVERS },
  'accepted_payload_size': { summary: 'Максимальный размер DNS-ответа по UDP (EDNS0).', syntax: 'accepted_payload_size <байт>', sections: RESOLVERS },
  peer: { summary: 'Участник синхронизации stick-table.', syntax: 'peer <имя> <адрес>:<порт>', sections: ['peers'] },
};

/** Arguments/options, grouped by the directive that owns them. */
const serverOptions: Record<string, HAProxyDoc> = {
  check: { summary: 'Включает health-check сервера. Упавший сервер исключается из балансировки.', syntax: 'server … check [inter <время>] [fall <n>] [rise <n>]', warning: '⚠ Если **check** не указан, то HAProxy шлёт трафик на сервер, даже если он недоступен.' },
  inter: { summary: 'Интервал между health-check.', syntax: 'inter <время>   # по умолчанию 2s' },
  fastinter: { summary: 'Интервал проверок во время перехода состояния.', syntax: 'fastinter <время>' },
  downinter: { summary: 'Интервал проверок, пока сервер лежит.', syntax: 'downinter <время>' },
  fall: { summary: 'Сколько подряд неудачных проверок до статуса DOWN.', syntax: 'fall <число>   # по умолчанию 3' },
  rise: { summary: 'Сколько подряд успешных проверок до статуса UP.', syntax: 'rise <число>   # по умолчанию 2' },
  weight: { summary: 'Вес сервера при балансировке: доля трафика пропорциональна весу. 0 — сервер не получает новых соединений.', syntax: 'weight <0-256>', warning: '⚠ Если NodeFlow управляет весами (leastping), то ручные **weight** перезапишутся Agent-ом.' },
  backup: { summary: 'Резервный сервер: получает трафик, только когда все основные недоступны.', syntax: 'server … backup' },
  'send-proxy': { summary: 'Отправлять серверу PROXY protocol v1 (текстовый) с реальным адресом клиента.', syntax: 'server … send-proxy', warning: '⚠ Если сервер **не ждёт** PROXY-заголовок, то все соединения к нему будут ломаться.' },
  'send-proxy-v2': { summary: 'Отправлять серверу PROXY protocol v2 (бинарный) с реальным адресом клиента.', syntax: 'server … send-proxy-v2', warning: '⚠ Если сервер **не ждёт** PROXY v2, то все соединения к нему будут ломаться.' },
  'check-send-proxy': { summary: 'Слать PROXY-заголовок и в health-check, когда сервер требует его на каждом соединении.', syntax: 'server … check send-proxy-v2 check-send-proxy', warning: '⚠ Если сервер ждёт PROXY, а **check-send-proxy** не указан, то проверки падают и сервер помечается DOWN.' },
  resolvers: { summary: 'Секция resolvers для перерезолва DNS-имени сервера во время работы.', syntax: 'resolvers <секция>' },
  'init-addr': { summary: 'Как получить адрес при старте: last — из state-файла, libc — системным резолвером, none — стартовать без адреса.', syntax: 'init-addr last,libc,none|<ip>', warning: '⚠ Если **none** не указан, а DNS недоступен при старте, то HAProxy не запустится.' },
  maxconn: { summary: 'Максимум одновременных соединений к этому серверу; остальные ждут в очереди backend.', syntax: 'maxconn <число>' },
  slowstart: { summary: 'Плавный разгон веса сервера после возврата в UP, чтобы не завалить его трафиком.', syntax: 'slowstart <время>' },
  'resolve-opts': { summary: 'Опции DNS-резолва сервера: prevent-dup-ip — не давать двум слотам один IP, allow-dup-ip, ignore-weight.', syntax: 'resolve-opts prevent-dup-ip[,…]' },
  'resolve-prefer': { summary: 'Предпочитаемое семейство адресов при резолве.', syntax: 'resolve-prefer ipv4|ipv6' },
  'hash-key': { summary: 'Что использовать как ключ сервера для consistent-хеша: id, addr или addr-port. addr сохраняет привязку при смене порядка слотов.', syntax: 'hash-key id|addr|addr-port' },
  disabled: { summary: 'Сервер в режиме обслуживания (MAINT) с момента запуска.', syntax: 'server … disabled' },
  ssl: { summary: 'Шифровать соединение к серверу TLS.', syntax: 'server … ssl [verify none|required]' },
  verify: { summary: 'Проверка сертификата сервера.', syntax: 'verify none|required' },
  sni: { summary: 'SNI для TLS-соединения к серверу.', syntax: 'sni <выражение>' },
  port: { summary: 'Порт для health-check, если отличается от порта трафика.', syntax: 'port <порт>' },
  addr: { summary: 'Адрес для health-check, если отличается от адреса трафика.', syntax: 'addr <адрес>' },
  'agent-check': { summary: 'Включить agent-check: внешний агент сообщает вес и состояние сервера.', syntax: 'agent-check agent-port <порт>' },
  'on-marked-down': { summary: 'Что делать при падении сервера: shutdown-sessions — закрыть его сессии.', syntax: 'on-marked-down shutdown-sessions' },
  'on-marked-up': { summary: 'Что делать при возврате сервера: shutdown-backup-sessions.', syntax: 'on-marked-up shutdown-backup-sessions' },
  observe: { summary: 'Пассивная проверка здоровья по ошибкам реального трафика.', syntax: 'observe layer4|layer7' },
  track: { summary: 'Брать состояние из другого сервера вместо своих проверок.', syntax: 'track [<backend>/]<сервер>' },
  cookie: { summary: 'Значение cookie для привязки в HTTP-режиме.', syntax: 'cookie <значение>' },
  id: { summary: 'Числовой идентификатор сервера.', syntax: 'id <число>' },
};

const bindOptions: Record<string, HAProxyDoc> = {
  'accept-proxy': { summary: 'Требовать PROXY protocol (v1/v2) от каждого входящего соединения — адрес клиента берётся из заголовка.', syntax: 'bind :443 accept-proxy', warning: '⚠ Если клиенты подключаются **напрямую**, без PROXY-заголовка, то их соединения будут отклонены.' },
  v4v6: { summary: 'Слушать IPv4 и IPv6 на одном сокете «::».', syntax: 'bind :::443 v4v6' },
  v6only: { summary: 'Сокет «::» принимает только IPv6.', syntax: 'bind :::443 v6only' },
  reuseport: { summary: 'В HAProxy это не опция bind: SO_REUSEPORT включён по умолчанию, а отключается глобальной директивой noreuseport.', syntax: 'global\n    noreuseport' },
  transparent: { summary: 'Слушать нелокальный адрес (TPROXY).', syntax: 'bind 10.0.0.1:443 transparent' },
  tfo: { summary: 'Разрешить TCP Fast Open для входящих соединений.', syntax: 'bind :443 tfo' },
  'defer-accept': { summary: 'Будить HAProxy только когда клиент прислал данные.', syntax: 'bind :443 defer-accept' },
  interface: { summary: 'Слушать только на указанном сетевом интерфейсе.', syntax: 'bind :443 interface eth0' },
  name: { summary: 'Имя слушающего сокета в статистике.', syntax: 'name <имя>' },
  ssl: { summary: 'Терминировать TLS на этом bind.', syntax: 'bind :443 ssl crt <файл>', warning: '⚠ Если включить **ssl** на SNI-маршрутизации NodeFlow, то HAProxy расшифрует трафик вместо прозрачной пересылки.' },
  crt: { summary: 'Сертификат (PEM с ключом) или каталог сертификатов для ssl.', syntax: 'crt <путь>' },
  alpn: { summary: 'Список ALPN-протоколов для TLS.', syntax: 'alpn h2,http/1.1' },
  thread: { summary: 'Какими потоками обслуживать этот bind.', syntax: 'thread <потоки>' },
  backlog: { summary: 'Очередь ядра для этого сокета.', syntax: 'backlog <число>' },
  maxconn: { summary: 'Максимум одновременных соединений через этот bind.', syntax: 'maxconn <число>' },
  mode: { summary: 'Права на UNIX-сокет (восьмеричные).', syntax: 'mode 660' },
  user: { summary: 'Владелец UNIX-сокета.', syntax: 'user <имя>' },
  group: { summary: 'Группа UNIX-сокета.', syntax: 'group <имя>' },
  level: { summary: 'Уровень доступа к stats-сокету: user, operator или admin.', syntax: 'level user|operator|admin', warning: '⚠ Если понизить **level admin**, то NodeFlow Agent не сможет управлять весами и квотами.' },
  admin: { summary: 'Полный уровень доступа к Runtime API.', syntax: 'level admin' },
  'expose-fd': { summary: 'Разрешить передачу слушающих сокетов новому процессу при reload — соединения не теряются.', syntax: 'expose-fd listeners', warning: '⚠ Если убрать **expose-fd listeners**, то при reload возможны отказы в соединении.' },
  listeners: { summary: 'Аргумент expose-fd: передавать слушающие сокеты.', syntax: 'expose-fd listeners' },
  namespace: { summary: 'Слушать в указанном network namespace.', syntax: 'namespace <имя>' },
};

const balanceArgs: Record<string, HAProxyDoc> = {
  roundrobin: { summary: 'По очереди с учётом весов; веса можно менять на лету.', syntax: 'balance roundrobin' },
  'static-rr': { summary: 'По очереди с учётом весов, без ограничения на число серверов; веса не меняются на лету.', syntax: 'balance static-rr' },
  leastconn: { summary: 'Сервер с наименьшим числом активных соединений (с учётом веса). Подходит для долгих TCP-сессий.', syntax: 'balance leastconn' },
  first: { summary: 'Первый сервер с свободными слотами maxconn; остальные включаются по мере заполнения.', syntax: 'balance first' },
  source: { summary: 'Хеш IP клиента: один клиент попадает на один сервер, пока набор серверов не меняется.', syntax: 'balance source', warning: '⚠ Если клиенты за **общим NAT**, то они все уйдут на один сервер.' },
  random: { summary: 'Случайный выбор; random(N) — лучший из N случайных по числу соединений («power of two choices»).', syntax: 'balance random[(<N>)]' },
  hash: { summary: 'Хеш произвольного выражения, например src,ipmask(32,64) — привязка по подсети.', syntax: 'balance hash <выражение>' },
  uri: { summary: 'Хеш URI запроса (HTTP).', syntax: 'balance uri' },
  hdr: { summary: 'Хеш HTTP-заголовка.', syntax: 'balance hdr(<имя>)' },
  'rdp-cookie': { summary: 'Хеш RDP-cookie.', syntax: 'balance rdp-cookie' },
};

const hashTypeArgs: Record<string, HAProxyDoc> = {
  'map-based': { summary: 'Статическая таблица: быстро, но при изменении набора серверов перераспределяется большинство клиентов.', syntax: 'hash-type map-based' },
  consistent: { summary: 'Кольцо хешей: при добавлении/удалении сервера переезжает лишь малая доля клиентов.', syntax: 'hash-type consistent' },
  sdbm: { summary: 'Хеш-функция sdbm (по умолчанию).', syntax: 'hash-type consistent sdbm' },
  djb2: { summary: 'Хеш-функция djb2.', syntax: 'hash-type consistent djb2' },
  wt6: { summary: 'Хеш-функция wt6.', syntax: 'hash-type consistent wt6' },
  crc32: { summary: 'Хеш-функция crc32.', syntax: 'hash-type consistent crc32' },
  none: { summary: 'Без хеш-функции: значение используется как есть.', syntax: 'hash-type consistent none' },
  avalanche: { summary: 'Дополнительное перемешивание хеша — равномернее распределяет похожие ключи (соседние IP).', syntax: 'hash-type consistent sdbm avalanche' },
};

const stickTableArgs: Record<string, HAProxyDoc> = {
  type: { summary: 'Тип ключа таблицы: ip, ipv6, integer, string, binary.', syntax: 'type ip|ipv6|integer|string [len <n>]' },
  ip: { summary: 'Ключ — IPv4-адрес клиента.', syntax: 'type ip' },
  ipv6: { summary: 'Ключ — IPv6-адрес (IPv4 хранится как отображённый).', syntax: 'type ipv6' },
  integer: { summary: 'Ключ — целое число.', syntax: 'type integer' },
  string: { summary: 'Ключ — строка.', syntax: 'type string len <n>' },
  size: { summary: 'Максимум записей; суффиксы k, m, g.', syntax: 'size <число>[k|m|g]', warning: '⚠ Если таблица **переполнена**, то вытесняются самые старые записи.' },
  expire: { summary: 'Время жизни неиспользуемой записи.', syntax: 'expire <время>' },
  peers: { summary: 'Секция peers для синхронизации таблицы; в NodeFlow сохраняет привязки при reload.', syntax: 'peers <секция>' },
  store: { summary: 'Какие счётчики хранить в записи.', syntax: 'store <счётчик>[,…]' },
  nopurge: { summary: 'Не вытеснять записи при переполнении.', syntax: 'nopurge' },
  bytes_in_rate: { summary: 'Скорость входящих байт за период — основа лимита upload.', syntax: 'store bytes_in_rate(<период>)' },
  bytes_out_rate: { summary: 'Скорость исходящих байт за период — основа лимита download.', syntax: 'store bytes_out_rate(<период>)' },
  conn_cur: { summary: 'Текущее число соединений ключа.', syntax: 'store conn_cur' },
  conn_rate: { summary: 'Скорость новых соединений ключа.', syntax: 'store conn_rate(<период>)' },
  server_id: { summary: 'ID сервера, к которому привязан ключ.', syntax: 'store server_id' },
};

const tcpRequestArgs: Record<string, HAProxyDoc> = {
  accept: { summary: 'Принять соединение и прекратить проверку остальных правил этого уровня.', syntax: 'tcp-request content accept [if <условие>]' },
  reject: { summary: 'Закрыть соединение.', syntax: 'tcp-request content reject [if <условие>]' },
  'expect-proxy': { summary: 'Ожидать PROXY protocol заголовок на этом соединении.', syntax: 'tcp-request connection expect-proxy layer4 [if <условие>]', warning: '⚠ Если заголовок ждётся от **всех** источников, то прямые клиенты без PROXY не подключатся.' },
  layer4: { summary: 'Уровень правила: сразу после accept().', syntax: 'expect-proxy layer4' },
  'set-bandwidth-limit': { summary: 'Включить фильтр bwlim для соединения, опционально с лимитом и периодом.', syntax: 'tcp-request content set-bandwidth-limit <фильтр> [limit <v>] [period <t>]' },
  'track-sc0': { summary: 'Учитывать соединение в stick-table (счётчик sc0).', syntax: 'track-sc0 <ключ> [table <таблица>]' },
  'track-sc1': { summary: 'Учитывать соединение в stick-table (счётчик sc1).', syntax: 'track-sc1 <ключ> [table <таблица>]' },
  'silent-drop': { summary: 'Молча сбросить соединение без RST клиенту.', syntax: 'tcp-request … silent-drop' },
  'set-var': { summary: 'Записать значение в переменную.', syntax: 'set-var(<имя>) <выражение>' },
  content: { summary: 'Уровень правил над содержимым (после inspect-delay).', syntax: 'tcp-request content …' },
  connection: { summary: 'Уровень правил сразу после accept().', syntax: 'tcp-request connection …' },
  session: { summary: 'Уровень правил после handshake.', syntax: 'tcp-request session …' },
};

const filterArgs: Record<string, HAProxyDoc> = {
  'bwlim-in': { summary: 'Ограничение скорости входящего (upload) трафика.', syntax: 'filter bwlim-in <имя> …' },
  'bwlim-out': { summary: 'Ограничение скорости исходящего (download) трафика.', syntax: 'filter bwlim-out <имя> …' },
  limit: { summary: 'Лимит скорости в байтах в секунду (суффиксы k, m, g).', syntax: 'limit <байт/с>' },
  key: { summary: 'Выражение-ключ, по которому считается лимит (обычно адрес клиента).', syntax: 'key <выражение>' },
  table: { summary: 'Stick-table, где хранится текущая скорость по ключу.', syntax: 'table <таблица>' },
  'min-size': { summary: 'Минимальный размер порции данных, который фильтр пропускает за раз.', syntax: 'min-size <байт>' },
  period: { summary: 'Период усреднения скорости.', syntax: 'period <время>' },
  'default-limit': { summary: 'Лимит по умолчанию для фильтра без ключа.', syntax: 'default-limit <байт/с>' },
  'default-period': { summary: 'Период по умолчанию.', syntax: 'default-period <время>' },
};

const holdArgs: Record<string, HAProxyDoc> = {
  valid: { summary: 'Сколько использовать успешный ответ DNS до обновления.', syntax: 'hold valid <время>' },
  nx: { summary: 'Сколько держать старый адрес при ответе NXDOMAIN.', syntax: 'hold nx <время>' },
  obsolete: { summary: 'Сколько держать адрес, исчезнувший из ответа DNS.', syntax: 'hold obsolete <время>' },
  timeout: { summary: 'Сколько держать старый адрес при таймауте DNS.', syntax: 'hold timeout <время>' },
  refused: { summary: 'Сколько держать старый адрес при ответе REFUSED.', syntax: 'hold refused <время>' },
  other: { summary: 'Сколько держать старый адрес при прочих ошибках DNS.', syntax: 'hold other <время>' },
};

const logArgs: Record<string, HAProxyDoc> = {
  global: { summary: 'Использовать log-цели из секции global.', syntax: 'log global' },
  notice: { summary: 'Уровень syslog notice: только значимые события (смена состояния серверов).', syntax: 'log <адрес> <facility> notice' },
  info: { summary: 'Уровень syslog info: включает лог каждого соединения.', syntax: 'log <адрес> <facility> info' },
  local0: { summary: 'Syslog facility local0 — лог соединений HAProxy.', syntax: 'log /dev/log local0' },
  local1: { summary: 'Syslog facility local1.', syntax: 'log /dev/log local1 notice' },
};

const modeArgs: Record<string, HAProxyDoc> = {
  tcp: { summary: 'Прозрачная TCP-пересылка без разбора протокола. Так работают все маршруты NodeFlow.', syntax: 'mode tcp' },
  http: { summary: 'Разбор HTTP: заголовки, http-request правила, cookies.', syntax: 'mode http' },
};

const initAddrArgs: Record<string, HAProxyDoc> = {
  last: { summary: 'Взять последний известный адрес из state-файла.', syntax: 'init-addr last' },
  libc: { summary: 'Резолвить системным резолвером при старте.', syntax: 'init-addr libc' },
  none: { summary: 'Разрешить старт без адреса: сервер в MAINT, пока DNS не ответит.', syntax: 'init-addr none' },
};

export const argDocs: Record<string, Record<string, HAProxyDoc>> = {
  server: serverOptions,
  'server-template': serverOptions,
  'default-server': serverOptions,
  bind: bindOptions,
  'stats socket': bindOptions,
  balance: balanceArgs,
  'hash-type': hashTypeArgs,
  'stick-table': stickTableArgs,
  'tcp-request': tcpRequestArgs,
  filter: filterArgs,
  hold: holdArgs,
  log: logArgs,
  mode: modeArgs,
  'init-addr': initAddrArgs,
  'resolve-opts': {
    'prevent-dup-ip': { summary: 'Не назначать один и тот же IP двум слотам server-template — каждый адрес DNS-записи получает свой слот.', syntax: 'resolve-opts prevent-dup-ip' },
    'allow-dup-ip': { summary: 'Разрешить нескольким слотам получить один IP.', syntax: 'resolve-opts allow-dup-ip' },
    'ignore-weight': { summary: 'Не брать вес из SRV-записей DNS.', syntax: 'resolve-opts ignore-weight' },
  },
  'hash-key': {
    id: { summary: 'Ключ consistent-хеша — числовой ID сервера.', syntax: 'hash-key id' },
    addr: { summary: 'Ключ consistent-хеша — IP сервера: привязка клиентов сохраняется, даже если слот сменил адрес.', syntax: 'hash-key addr' },
    'addr-port': { summary: 'Ключ consistent-хеша — IP и порт сервера.', syntax: 'hash-key addr-port' },
  },
};

/** Tokens meaningful anywhere: conditions, fetches, ACL flags, functions. */
export const keywordDocs: Record<string, HAProxyDoc> = {
  '{': { summary: 'Начало анонимного ACL: условие записывается прямо в правиле, без отдельной строки acl.', syntax: 'if { <выборка> [флаги] <значения> }', warning: '⚠ Если забыть **пробел** после { или перед }, то HAProxy не распознает условие.' },
  '}': { summary: 'Конец анонимного ACL.', syntax: 'if { <выборка> <значения> }' },
  if: { summary: 'Правило срабатывает, только если условие истинно. Несколько ACL подряд — «И», «||» или or — «ИЛИ».', syntax: '<правило> if <acl> [<acl>…] [|| <acl>]' },
  unless: { summary: 'Правило срабатывает, только если условие ложно.', syntax: '<правило> unless <acl>' },
  or: { summary: 'Логическое «ИЛИ» между ACL в условии.', syntax: 'if acl1 or acl2' },
  '||': { summary: 'Логическое «ИЛИ» между ACL в условии.', syntax: 'if acl1 || acl2' },
  '!': { summary: 'Отрицание ACL.', syntax: 'if !acl1' },
  src: { summary: 'Выборка: IP-адрес клиента (после PROXY protocol — реальный адрес из заголовка).', syntax: 'acl <имя> src <сеть>[ …] | -f <файл>' },
  dst: { summary: 'Выборка: локальный IP, на который пришло соединение.', syntax: 'acl <имя> dst <адрес>' },
  dst_port: { summary: 'Выборка: локальный порт, на который пришло соединение.', syntax: 'acl <имя> dst_port <порт>[:<порт>]' },
  src_port: { summary: 'Выборка: порт клиента.', syntax: 'acl <имя> src_port <порт>' },
  'req.ssl_sni': { summary: 'Выборка: имя сервера (SNI) из TLS ClientHello без расшифровки. Требует tcp-request inspect-delay.', syntax: 'acl <имя> req.ssl_sni -i <домен>[ …]', warning: '⚠ Если клиент **не передаёт SNI** (старый клиент, IP вместо домена), то сработает default_backend.' },
  'req_ssl_sni': { summary: 'Устаревшее имя req.ssl_sni: SNI из TLS ClientHello.', syntax: 'req_ssl_sni -i <домен>' },
  'req.ssl_hello_type': { summary: 'Выборка: тип TLS handshake-сообщения; 1 — ClientHello. Используется, чтобы принять соединение, как только SNI получен.', syntax: 'tcp-request content accept if { req.ssl_hello_type 1 }' },
  'req.len': { summary: 'Выборка: сколько байт запроса уже получено в буфере.', syntax: 'req.len gt 0' },
  'req.ssl_ver': { summary: 'Выборка: версия TLS из ClientHello.', syntax: 'req.ssl_ver ge 3.3' },
  'ssl_fc_sni': { summary: 'Выборка: SNI при TLS-терминации на bind ssl.', syntax: 'ssl_fc_sni -i <домен>' },
  'sc0_bytes_out_rate': { summary: 'Скорость исходящих байт по счётчику sc0.', syntax: 'sc0_bytes_out_rate gt <n>' },
  ipmask: { summary: 'Преобразователь: маскирует адрес до сети (IPv4-префикс, IPv6-префикс). src,ipmask(32,64) — привязка по /64 для IPv6.', syntax: 'src,ipmask(<v4-маска>[,<v6-маска>])' },
  '-i': { summary: 'Флаг ACL: сравнение без учёта регистра.', syntax: 'acl <имя> <выборка> -i <значения>' },
  '-m': { summary: 'Флаг ACL: метод сравнения — str, beg, end, sub, reg, found, ip, int…', syntax: 'acl <имя> <выборка> -m <метод> <значения>' },
  '-f': { summary: 'Флаг ACL: загрузить значения из файла (по одному на строку).', syntax: 'acl <имя> src -f <файл>', warning: '⚠ Если **файла нет** на ноде, то HAProxy не загрузит конфиг.' },
  '-n': { summary: 'Флаг ACL: не резолвить DNS-имена в значениях.', syntax: 'acl <имя> src -n <значения>' },
  enabled: { summary: 'Функция условия .if: истинна, если возможность (например SPLICE) поддерживается и не выключена.', syntax: '.if enabled(<возможность>)' },
  defined: { summary: 'Функция условия .if: истинна, если переменная окружения определена.', syntax: '.if defined(<переменная>)' },
  version_atleast: { summary: 'Функция условия .if: версия HAProxy не ниже указанной.', syntax: '.if version_atleast(2.8)' },
  feature: { summary: 'Функция условия .if: сборка поддерживает возможность.', syntax: '.if feature(OPENSSL)' },
};

/** Tokens that take a value right after them (the value gets a duration/number doc). */
export const DURATION_UNITS: Record<string, string> = { us: 'микросекунд', ms: 'миллисекунд', s: 'секунд', m: 'минут', h: 'часов', d: 'дней' };
