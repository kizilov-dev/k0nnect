# Конфигурация

CLI читает JSON-файл соединения. Один и тот же файл используется для проверки, подключения, установки аддона и удаления.

## Пример

Адрес и fingerprint ниже — заглушки. Подставьте реальный адрес VDS и проверенный SSH fingerprint.

```json
{
  "name": "external-worker",
  "nodeName": "k8nnect-vds",
  "namespace": "k8nnect-system",
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

## Имена и доступ

| Поле | Значение |
| --- | --- |
| `name` | Имя соединения; используется для меток и проверки владения |
| `nodeName` | Имя нового Kubernetes Node |
| `namespace` | Пространство имён аддона; по умолчанию `k8nnect-system` |
| `host` | IPv4-адрес VDS; DNS-имена пока не поддержаны |
| `user` | Сейчас только `root` |
| `sshPort` | SSH-порт; по умолчанию 22 |
| `hostKeySHA256` | Закреплённый fingerprint; текущий preflight использует Ed25519 |
| `passwordFile` | Локальный файл с паролем |
| `privateKeyFile` | Альтернатива паролю: путь к SSH private key без passphrase |

Имена начинаются с латинской буквы в нижнем регистре, затем допускают буквы, цифры и дефис; максимум 41 символ.

## Адреса и MTU

| Поле | Ограничение |
| --- | --- |
| `transportCIDR` | Приватная каноническая IPv4 /24 без пересечений |
| `workerAddress` | Внутри transportCIDR; последний октет 100–254 |
| `port` | UDP NodePort gateway: 30000–32766; по умолчанию 31820 |
| `mtu` | 1280–1440; по умолчанию 1400 |
| `k0sVersion` | Необязательная версия k0s; обычно определяется из worker-профиля |

На VDS WireGuard слушает `port + 1`. Первые адреса /24 выделяются gateway-нодам, поэтому диапазон Worker начинается с `.100`. Не меняйте адрес уже присоединённой ноды: сначала выполните контролируемое отключение.

MTU WireGuard не заменяет настройки CNI. Вложенный VXLAN и фрагментация проверяются отдельными TCP/UDP-тестами.

## Где хранятся настройки

Аддон монтирует публичную конфигурацию из ConfigMap `connection`, а SSH-креды — из Secret `vds-ssh`. Статус находится в ConfigMap `connection-status`.

На VDS каталог `/etc/k8nnect` содержит конфигурацию сети, приватный ключ WireGuard, маркер владения, resolver и bootstrap-файл. Доступ к этим данным нужен только root.
