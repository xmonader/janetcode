package typosquat

import "strings"

// The well-known names, per ecosystem: the packages a squatter imitates,
// because they are the ones a model and a person type from memory. The
// lists are short on purpose — a long list is more near-neighbours to
// trip over — and a project's own dependencies and module cache add the
// rest. Real packages that sit one keystroke from another real one
// (react/preact, request/requests) are both listed, so neither flags.

var popularGo = words(`
github.com/stretchr/testify github.com/sirupsen/logrus github.com/spf13/cobra github.com/spf13/viper
github.com/spf13/pflag github.com/gin-gonic/gin github.com/gorilla/mux github.com/gorilla/websocket
github.com/go-chi/chi github.com/labstack/echo github.com/gofiber/fiber github.com/google/uuid
github.com/google/go-cmp github.com/pkg/errors github.com/joho/godotenv github.com/golang-jwt/jwt
github.com/go-redis/redis github.com/redis/go-redis github.com/lib/pq github.com/jackc/pgx
github.com/go-sql-driver/mysql github.com/mattn/go-sqlite3 github.com/boltdb/bolt go.etcd.io/bbolt
github.com/BurntSushi/toml github.com/pelletier/go-toml github.com/fatih/color github.com/urfave/cli
github.com/prometheus/client_golang github.com/rs/zerolog go.uber.org/zap go.uber.org/multierr
github.com/charmbracelet/bubbletea github.com/charmbracelet/lipgloss github.com/mattn/go-isatty
github.com/hashicorp/go-multierror github.com/hashicorp/golang-lru github.com/cenkalti/backoff
github.com/golang/protobuf google.golang.org/protobuf google.golang.org/grpc github.com/aws/aws-sdk-go
github.com/aws/aws-sdk-go-v2 github.com/docker/docker github.com/gorilla/sessions github.com/olekukonko/tablewriter
github.com/shopspring/decimal github.com/valyala/fasthttp github.com/json-iterator/go github.com/tidwall/gjson
github.com/mitchellh/mapstructure github.com/go-playground/validator github.com/dgrijalva/jwt-go
golang.org/x/net golang.org/x/sys golang.org/x/text golang.org/x/crypto golang.org/x/sync golang.org/x/tools
golang.org/x/mod golang.org/x/exp golang.org/x/term golang.org/x/oauth2 golang.org/x/time
gopkg.in/yaml.v2 gopkg.in/yaml.v3 gorm.io/gorm github.com/fsnotify/fsnotify github.com/robfig/cron
`)

var popularNPM = words(`
react react-dom preact next vue nuxt angular svelte express koa fastify hapi lodash underscore
axios request requests node-fetch got superagent moment dayjs date-fns luxon chalk colors commander
yargs minimist debug dotenv cross-env cross-spawn uuid nanoid classnames prop-types redux react-redux
mobx rxjs typescript ts-node tslib webpack vite rollup esbuild babel-core @babel/core babel-cli
@babel/cli eslint prettier jest mocha chai sinon vitest cypress playwright puppeteer electron
jquery bootstrap tailwindcss postcss autoprefixer sass node-sass less socket.io ws mongoose mongodb
mysql mysql2 pg sequelize typeorm prisma knex redis ioredis sqlite3 better-sqlite3 nodemailer
body-parser cors helmet morgan cookie-parser express-session passport jsonwebtoken bcrypt bcryptjs
multer sharp jimp canvas d3 three chart.js fabric ffmpeg fluent-ffmpeg opencv js-yaml yaml
glob rimraf mkdirp fs-extra chokidar semver inquirer ora shelljs execa zx async bluebird q
event-stream through2 graphql apollo-server handlebars ejs pug mustache marked highlight.js
xml2js cheerio jsdom crypto-js node-forge validator joi yup zod ajv immutable ramda
coffee-script coffeescript grunt grunt-cli gulp gulp-cli bower nodemon pm2 forever
http-proxy http-server serve concurrently npm-run-all husky lint-staged
`)

var popularPyPI = words(`
requests urllib3 certifi idna charset-normalizer numpy pandas scipy matplotlib seaborn scikit-learn
tensorflow torch keras pillow opencv-python beautifulsoup4 bs4 lxml html5lib selenium scrapy
flask django fastapi starlette uvicorn gunicorn jinja2 werkzeug click typer rich colorama
python-dateutil pytz six setuptools wheel pip virtualenv attrs pydantic sqlalchemy psycopg2
psycopg2-binary pymysql mysqlclient redis celery boto3 botocore awscli pyyaml toml tomli
pytest pytest-cov mock coverage tox nose black flake8 pylint mypy isort jsonschema
cryptography pycryptodome paramiko pyjwt bcrypt passlib httpx aiohttp websockets grpcio protobuf
tqdm jellyfish nltk spacy transformers openai anthropic sympy networkx simplejson ujson
docutils sphinx markdown pygments termcolor tabulate prettytable python-dotenv dotenv
pyopenssl openpyxl xlrd xlwt docx python-docx reportlab pyserial pyusb psutil
setproctitle gevent eventlet twisted tornado zope-interface pycparser cffi
`)

// popularPyImport are the names Python code imports, where they differ
// from the distribution's (import yaml for PyYAML) or are the same.
var popularPyImport = words(`
requests urllib3 certifi idna numpy pandas scipy matplotlib seaborn sklearn tensorflow torch keras
PIL cv2 bs4 lxml selenium scrapy flask django fastapi starlette uvicorn jinja2 werkzeug click typer
rich colorama dateutil pytz six setuptools attr attrs pydantic sqlalchemy psycopg2 pymysql MySQLdb
redis celery boto3 botocore yaml toml tomli tomllib pytest mock coverage jsonschema cryptography
Crypto paramiko jwt bcrypt passlib httpx aiohttp websockets grpc google tqdm jellyfish nltk spacy
transformers openai anthropic sympy networkx simplejson ujson docutils sphinx markdown pygments
termcolor tabulate prettytable dotenv OpenSSL openpyxl xlrd docx reportlab serial usb psutil
gevent twisted tornado zope cffi
`)

// pyStdlib is the standard library a script imports from most; a near
// miss of one (jsno, colections) is a typo the build would catch, but
// one somebody may have registered on PyPI.
var pyStdlib = words(`
abc argparse array ast asyncio base64 binascii bisect builtins bz2 calendar cmath codecs collections
concurrent configparser contextlib contextvars copy csv ctypes dataclasses datetime decimal difflib
dis email enum errno faulthandler fcntl filecmp fileinput fnmatch fractions ftplib functools gc
getopt getpass gettext glob gzip hashlib heapq hmac html http imaplib importlib inspect io ipaddress
itertools json keyword linecache locale logging lzma mailbox math mimetypes mmap multiprocessing
netrc numbers operator optparse os pathlib pdb pickle pkgutil platform plistlib poplib posix pprint
profile pstats pty pwd queue random re readline reprlib resource secrets select selectors shelve
shlex shutil signal site smtplib socket socketserver sqlite3 ssl stat statistics string struct
subprocess sys sysconfig syslog tarfile tempfile termios textwrap threading time timeit tkinter
token tokenize tomllib trace traceback tracemalloc types typing unicodedata unittest urllib uuid
venv warnings wave weakref webbrowser xml xmlrpc zipfile zipimport zlib zoneinfo __future__
`)

// nodeBuiltins are Node's own modules, imported bare.
var nodeBuiltins = words(`
assert async_hooks buffer child_process cluster console constants crypto dgram diagnostics_channel
dns domain events fs http http2 https inspector module net os path perf_hooks process punycode
querystring readline repl stream string_decoder sys timers tls trace_events tty url util v8 vm
wasi worker_threads zlib test
`)

func words(s string) []string { return strings.Fields(s) }
