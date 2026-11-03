# Быстрый старт

От SSH-доступа к серверу до проверенной Kubernetes-ноды. Прежде чем начать, сверьтесь с [матрицей совместимости](compatibility.md): контроллер принимает только поддержанный профиль.

Контроллер работает в самом кластере. Клонировать и собирать ничего не нужно: достаточно `kubectl`, администраторского kubeconfig и root-доступа по SSH к серверу.

## 1. Установить контроллер

```sh
kubectl apply -f https://github.com/kizilov-dev/k0nnect/releases/latest/download/install.yaml
kubectl -n k0nnect-system rollout status deployment/controller
```

Манифест создаёт namespace `k0nnect-system`, CRD `vdsconnections.k0nnect.io`, ServiceAccount, статический RBAC и Deployment контроллера. Образ `ghcr.io/kizilov-dev/k0nnect` закреплён по digest. Права в файле можно проверить до установки — см. [права и секреты](security.md).

Пока первый релиз не опубликован, соберите образ и манифест из исходников: `make image IMAGE=registry.example/k0nnect:dev`, загрузите образ в registry, затем `make manifest IMAGE=registry.example/k0nnect:dev > install.yaml`.

## 2. Сохранить SSH-доступ

```sh
kubectl -n k0nnect-system create secret generic vds-ssh \
  --from-file=private-key=./id_ed25519
```

Ключ должен быть без passphrase. Для входа по паролю используйте ключ `password`, например `--from-file=password=./vds-password`. Контроллер читает этот Secret по имени через API; в Pod он не монтируется.

## 3. Описать сервер

Получите Ed25519 fingerprint SSH-ключа сервера через доверенный канал: например, в консоли провайдера или командой `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` на самом сервере.

```yaml
apiVersion: k0nnect.io/v1alpha1
kind: VDSConnection
metadata:
  name: vds
spec:
  host: 203.0.113.10
  hostKeySHA256: SHA256:REPLACE_WITH_VERIFIED_ED25519_FINGERPRINT
  transportCIDR: 10.203.74.0/24
  workerAddress: 10.203.74.100
```

```sh
kubectl apply -f vdsconnection.yaml
```

Объект должен называться `vds`: контроллер управляет одной VDS на кластер. `spec` после создания не меняется; чтобы изменить настройки, удалите объект и создайте новый. Все поля описаны в [конфигурации](configuration.md).

## 4. Следить за подключением

```sh
kubectl get vdsconnection
kubectl -n k0nnect-system logs -f deployment/controller
```

`Phase` переходит из `Connecting` в `Ready`; при `Error` причина указана в `Message`. Первая установка загружает пакеты и образы и занимает несколько минут. Для kube-router может понадобиться ещё один интервал синхронизации маршрутов.

```sh
kubectl get node k0nnect-vds -o wide
```

Обычные workloads попадают на ноду только после тестов Pod, когда снят taint `k0nnect.io/initializing`.

## Однократное подключение с рабочей машины

CLI подключает сервер без установки контроллера. Скачайте `k0nnect-linux-amd64` из [релизов](https://github.com/kizilov-dev/k0nnect/releases) или соберите `make build` (Linux, Go ≥ 1.26).

```sh
install -d -m 700 /secure
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

Укажите адрес и fingerprint, а для доступа — файл пароля `0600` в `passwordFile` или ключ в `privateKeyFile`. CLI также умеет работать через SSH agent.

```sh
k0nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
k0nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json \
  --output /secure/connect-review.json
k0nnect connect --kubeconfig /secure/cluster.yaml --config /secure/connection.json
```

`connect-check` ничего не меняет. `connect-plan` отправляет в API только GET, не использует SSH и не сохраняет секреты. `connect` выполняет ту же установку и проверку, что и контроллер. После неё сеть на VDS поддерживают systemd-сервисы, но в кластере за подключением никто не следит. См. [эксплуатацию](operations.md).
