# Конфигурация

Контроллер читает объект `VDSConnection` и Secret `vds-ssh`. CLI читает JSON-файл соединения с теми же сетевыми настройками.

## VDSConnection

Адрес и fingerprint ниже — заглушки. Подставьте реальный адрес VDS и проверенный SSH fingerprint.

```yaml
apiVersion: k0nnect.io/v1alpha1
kind: VDSConnection
metadata:
  name: vds
spec:
  host: 203.0.113.10
  sshPort: 22
  hostKeySHA256: SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT
  transportCIDR: 10.203.74.0/24
  workerAddress: 10.203.74.100
  port: 31820
  mtu: 1400
```

Объект кластерный и должен называться `vds`. Имена фиксированы, чтобы манифест установки ограничивал RBAC по именам: нода — `k0nnect-vds`, namespace — `k0nnect-system`. `spec` не меняется после создания, поэтому отключение всегда идёт на тот сервер, где установлен Worker.

## Secret vds-ssh

| Ключ | Значение |
| --- | --- |
| `private-key` | SSH private key без passphrase |
| `password` | SSH-пароль как альтернатива ключу |

Secret находится в `k0nnect-system`. Пользователь SSH — `root`.

## Адреса и порты

| Поле | Ограничение |
| --- | --- |
| `host` | IPv4-адрес VDS; DNS-имена пока не поддержаны |
| `sshPort` | SSH-порт; по умолчанию 22 |
| `hostKeySHA256` | Обязательный закреплённый fingerprint; текущий preflight использует Ed25519 |
| `transportCIDR` | Приватная каноническая IPv4 /24 без пересечений |
| `workerAddress` | Внутри transportCIDR; последний октет 100–254 |
| `port` | UDP-порт gateway из диапазона NodePort: 30000–32766; по умолчанию 31820 |
| `mtu` | MTU транспорта WireGuard: 1280–1440; по умолчанию 1400 |
| `k0sVersion` | Необязательная версия k0s; обычно определяется из worker-профиля |

На VDS WireGuard слушает `port + 1`. Первые адреса /24 выделяются gateway-нодам, поэтому диапазон Worker начинается с `.100`.

## Файл соединения CLI

```json
{
  "name": "external-worker",
  "nodeName": "k0nnect-vds",
  "namespace": "k0nnect-system",
  "host": "203.0.113.10",
  "user": "root",
  "sshPort": 22,
  "passwordFile": "/secure/vds-password",
  "hostKeySHA256": "SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT",
  "transportCIDR": "10.203.74.0/24",
  "workerAddress": "10.203.74.100",
  "port": 31820,
  "mtu": 1400
}
```

| Поле | Значение |
| --- | --- |
| `name` | Имя соединения; используется для меток и проверки владения |
| `nodeName` | Имя нового Kubernetes Node |
| `namespace` | Namespace, который CLI создаёт для соединения; по умолчанию `k0nnect-system` |
| `user` | Сейчас только `root` |
| `passwordFile` | Локальный файл с паролем |
| `privateKeyFile` | Альтернатива паролю: путь к SSH private key без passphrase |
| `image` | Необязательный образ k0nnect для gateway-агентов, например релизный |

Адресные поля — как в таблице выше. Имена начинаются с латинской буквы в нижнем регистре, затем допускают буквы, цифры и дефис; максимум 41 символ. Без `image` CLI передаёт собственный бинарник через ConfigMap в контейнеры `ubuntu:24.04`, которые при старте ставят сетевые утилиты. Не меняйте адрес уже присоединённой ноды: сначала выполните контролируемое отключение.

## MTU

`mtu` задаёт MTU интерфейса gateway `k0ngw0` и маршрута по умолчанию в namespace Worker. Сам интерфейс `eth1` на VDS получает MTU сети исходных нод: gateway-агенты публикуют его в ConfigMap `gateway-state` своего namespace, контроллер берёт минимум. CNI вычисляет MTU Pod от `eth1`, поэтому Pod на VDS получают тот же MTU, что и на остальных нодах. Иначе пакеты, которые больше MTU Pod на VDS, молча отбрасывались бы после туннеля CNI. Внешние пакеты больше транспортного MTU фрагментирует WireGuard.

Настройки MTU самого CNI не меняются. Вложенная инкапсуляция и фрагментация проверяются отдельными TCP/UDP-тестами.

## Где хранится состояние

Контроллер записывает ход подключения в `status` объекта VDSConnection. Настройки gateway и опубликованные ключи лежат в ConfigMap `gateway-network` и `gateway-state` в `k0nnect-system`.

На VDS каталог `/etc/k0nnect` содержит конфигурацию сети, приватный ключ WireGuard, маркер владения, resolver и bootstrap-файл. Доступ к этим данным нужен только root.
