# Быстрый старт

От SSH-доступа к серверу до проверенной Kubernetes-ноды. Прежде чем начать, сверитесь с [матрицей совместимости](compatibility.md): установщик принимает только поддержанный профиль.

## 1. Собрать CLI

Нужны Linux, Go ≥ 1.26 и установленный `kubectl`.

```sh
git clone https://github.com/kizilov-dev/k0nnect.git
cd k0nnect
make build
make plugin
export PATH="$PWD/bin:$PATH"
k0nnect connect --help
```

`make plugin` создаёт `bin/kubectl-k0nnect`. Команды можно вызывать и как `kubectl k0nnect`.

## 2. Подготовить доступ

Создайте локальный каталог вне репозитория. В примерах это `/secure`; замените его своим путём.

```sh
install -d -m 700 /secure
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

Укажите адрес сервера и независимо проверенный Ed25519 fingerprint его SSH-ключа. Пароль храните в отдельном файле `0600`, на который ссылается `passwordFile`. Вместо пароля можно использовать `privateKeyFile` с незашифрованным SSH-ключом. Не добавляйте секреты в Git.

Аддон требует файл пароля или ключа. SSH agent доступен только локальному CLI и не передаётся в кластер.

## 3. Проверить без установки

```sh
k0nnect connect-check \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Команда читает API и состояние VDS через закреплённый SSH-ключ. Она не устанавливает пакеты и не изменяет сеть. Поле `cni` в выводе показывает определённый CNI; если его настройки несовместимы, ошибка называет конкретный параметр (см. [совместимость](compatibility.md)).

Затем создайте план:

```sh
k0nnect connect-plan \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json \
  --output /secure/connect-review.json
```

План формируется локально. Все записи перехватываются в памяти, к реальному API идут только GET, SSH не используется. Значения Secret и исполняемый payload удалены. Этот JSON предназначен для ревью, а не для `kubectl apply`.

## 4. Установить аддон

```sh
k0nnect addon-install \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Аддон создаст Deployment, SSH Secret, конфигурацию и явные RBAC-права. Во время подключения он установит gateway-агенты и зарегистрирует VDS. Администраторский kubeconfig на сервер не копируется.

```sh
kubectl --kubeconfig /secure/cluster.yaml \
  -n k0nnect-system logs -f deployment/installer
```

## 5. Проверить результат

```sh
kubectl --kubeconfig /secure/cluster.yaml get node k0nnect-vds -o wide
kubectl --kubeconfig /secure/cluster.yaml \
  -n k0nnect-system get configmap connection-status -o yaml
```

Ожидайте Ready и сообщение `Worker connected and verified`. Начальный taint снимается только после тестов Pod. Системные образы и пакеты загружаются при первой установке, поэтому процесс занимает несколько минут.

## Однократное подключение

Если управляющий Deployment не нужен, вместо `addon-install` выполните:

```sh
k0nnect connect \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Это тот же механизм установки и проверки. На VDS останутся systemd-сервисы сети и Worker. Для последующего наблюдения используйте [руководство по эксплуатации](operations.md).
