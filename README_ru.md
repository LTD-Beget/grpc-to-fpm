# GRPC-TO-FPM
Прокси сервер `GRPC <=> FastCGI`.

## Почему

Если обратиться к документации gRPC, то нас ожидает облом:
из-за особенностей модели выполнения кода PHP не может выступать
в роли сервера. Зато он неплохо работает в роли клиента и,
если скормить генератору кода proto-файл с описанием сервиса,
он честно сгенерирует классы для всех структур (request и response).

### Схема работы:
* Принимаем gRPC-запрос;
* Вынимаем из него тело, которое сериализовано в Protobuf;
* Формируем заголовки для FastCGI-запроса;
* Отправляем FastCGI-запрос в PHP-FPM;
* В PHP обрабатываем запрос. Формируем ответ;
* Получаем ответ, конвертируем в gRPC, отправляем адресату;

## Установка

### Требования
* Go 1.8 и выше
* PHP-FPM

### Запуск

`go get -u github.com/LTD-Beget/grpc-to-fpm/cmd/grpc-to-fpm`

`./grpc-to-fpm`

### Режим отладки
Для получения большего числа логов, можно включить отладочный режим
с помощью сигнала `SIGUSR2`.

## Использование

### Конфигурация
Загружает из файла "grpc-to-fpm.yml".
Пример конфигурации:
```
instancename: my-php-service          # Название сервиса. Преимущественно для логов
host: ":50051"                        # Хост на котором будут приниматься запросы
debug: false                          # Дебаг режим, так-же переключается через SIGUSR2

target:
    host: localhost                       # Адрес PHP-FPM
    port: 9000                            # Порт PHP-FPM
    scriptpath: /home/myuser/app/handlers # Параметры для вызова PHP
    scriptname: index.php
    clientip: 127.0.0.1
    returnerror: true                     # Возвращать ошибку в полном виде?

# Конфигурация грейлога, если нет - грейлог использован не будет
graylog:
  host: graylog.localhost
  port: 12201

# Ключ и сертифкат файлы для использование TLS вместо обычного TCP при приеме GRPC
keyFile: localhost.key
crtFile: localhost.pem
```

Так-же все параметры доступны для переопределния с помощью ENV переменных:
Например `CONFIGOR_KEYFILE=localhost.key`

### Пример

Допустим у нас есть обработчик вида:
```
<?php
// Полное имя метода, который вызываем через gRPC вида:
// package.service-name/method-name
$route = $_GET['r'];

// Тело запроса, сериализованное в protobuf
$body = file_get_contents("php://input");

try {
    // Для демонстрации ошибки геренируем ее через раз
    if (rand(0, 1)) {
        throw new \RuntimeException("Some error happened!");
    }

    // Далее просто десериализуем тело запроса в сгенерированную структуру, выполняем
    // бизнес-логику, заполняем ответную структуру, сериализуем ее и отправляем в кач-ве ответа
} catch (\Throwable $e) {
    // Валидный статус-код.
    // Доступные коды можно посмотреть, например тут:
    // https://github.com/grpc/grpc-go/blob/master/codes/codes.go
    $errorCode = 13;

    header("X-Grpc-Status: ERROR");
    header("X-Grpc-Error-Code: {$errorCode}");
    header("X-Grpc-Error-Description: {$e->getMessage()}");
}
```

который мы кладем по пути `/home/myuser/app/handlers/index.php` и конфиг
который указан выше. Тогда вся работа сводится к запуску `PHP-FPM`
и непосредственно самой прокси.
