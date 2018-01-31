# SVC-GRPC-PROXY
Прототип прокси `GRPC < --- > FastCGI`. Для управления зависимостями используется github.com/LK4D4/vndr

## Конфигурация
Загружает из файла "grpc-proxy-config.yml".
Пример конфигурации:
```
instancename: my-php-service          // Название сервиса. Преимущественно для логов
host: ":50051"                        // Хост на котором будут приниматься запросы
debug: false                          // Дебаг режим, так-же переключается через SIGUSR2

target:
    host: localhost                       // Адрес PHP-FPM
    port: 9000                            // Порт PHP-FPM
    scriptpath: /home/myuser/app/handlers // Параметры для вызова PHP
    scriptname: index.php
    clientip: 127.0.0.1
    returnerror: true                     // Возвращать ошибку в полном виде?

// Конфигурация грейлога, если нет - грейлог использован не будет
graylog:
  host: graylog.localhost
  port: 12201

// Ключ и сертифкат файлы для использование TLS вместо обычного TCP при приеме GRPC
keyFile: localhost.key
crtFile: localhost.pem
```

Так-же все параметры доступны для переопределния с помощью ENV переменных:
Например `CONFIGOR_KEYFILE=localhost.key`

