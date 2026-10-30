# Диагностика

Начните с `connection-status`, логов установщика и состояния Pod. Статус Ready одной ноды недостаточен для подтверждения всей сети.

## SSH host key mismatch

Убедитесь, что адрес VDS правильный, а Ed25519 fingerprint получен через доверенный канал. Не подменяйте закреплённый ключ только потому, что подключение не прошло. Текущая CLI-проверка явно предпочитает Ed25519.

## Неподдерживаемая конфигурация CNI

`connect-check` выводит поле `cni` с определённым драйвером, а ошибка называет конкретную настройку. Требования для Calico, Flannel, Cilium и kube-router перечислены в [матрице совместимости](compatibility.md). Установщик специально отвергает другие настройки: менять CNI всего кластера ради обхода проверки не следует.

`no supported CNI found` означает, что не найден ни один из известных DaemonSet: `calico-node`, `kube-flannel-ds`, `cilium` или `kube-router`.

## Подготовка VDS завершилась с кодом 100

Это код ошибки apt. На свежей VDS сразу после загрузки работают cloud-init и первый проход unattended-upgrades, а зеркала провайдера иногда отказывают в соединении. Установщик ждёт блокировку dpkg и повторяет установку пакетов пять раз. Если ошибка осталась, выполните `apt-get update` на VDS вручную и проверьте доступ к зеркалу.

## Gateway Pod не Ready

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get events
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system \
  logs -l app=k8nnect-gateway --tail=100 --prefix
```

Проверьте доступ к образу Ubuntu и пакетным репозиториям, поддержку WireGuard, NET_ADMIN и включённый CNI IPv4 forwarding. Наличие процесса не заменяет успешную readiness probe.

`RTNETLINK answers: Address already in use` при `ip link set k8ngw0 up` означает, что UDP-порт gateway занят или запрещён. Cilium с kube-proxy replacement запрещает `bind()` на порты NodePort-сервисов; установщик для такого Cilium не создаёт Service `gateway`. Если ошибка осталась, найдите другой NodePort-сервис с тем же портом или смените `port`.

## Недоступен controller over gateway

Проверьте UDP между VDS и gateway, свежие handshakes и доступ gateway к приватным портам API и konnectivity. Для ноды без ExternalIP нужен исходящий UDP с ответным трафиком. Полный NAT traversal не предоставляется.

## Pod на VDS в ContainerCreating: failed to find plugin

`failed to find plugin "loopback" in path [/opt/cni/bin]` означает, что CNI не установил стандартные плагины. Установщик добавляет их сам, если их нет; на VDS, подключённой более старой версией, выполните `disconnect --drain` и повторите `connect`.

## k0s остановился после bootstrap

В k0s доступ к worker-профилям ограничен конкретными именами ConfigMap. Для клонированного профиля установщик создаёт отдельный Role/RoleBinding. Проверьте, что они появились и профиль не удалён.

Посмотрите `journalctl -u k0sworker`. Не выводите содержимое join token или kubelet kubeconfig в публичные issue.

## Нода Ready, но тесты не прошли

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get jobs,pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system logs job/verify-to-external
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system logs job/verify-from-external
```

Тест печатает конкретный этап: прямой Pod TCP, Service TCP, затем размеры UDP. Разделяйте DNS/Service, MTU и базовый node transport. При ошибке ресурсы сохраняются; следующий `verify-node` их очистит.

Сразу после регистрации CNI может ещё не настроить новую ноду на исходных: kube-router делает это при периодической синхронизации, до 5 минут. Проверка повторяется в этом окне и сообщает `retrying`; ошибка после окна окончательная. UDP-зонд отправляет каждую датаграмму до трёх раз: первая большая датаграмма может потеряться, пока отправитель не узнает MTU пути. В логе Job это строка `UDP retry after path MTU discovery`; для kube-router это ожидаемо.

## Конфликт владения или адресов

Установщик не перезаписывает чужой Node, namespace, интерфейс или конфигурацию существующего k0s. Проверьте имена и наличие старой установки. Для миграции управляемого адреса сначала используйте `disconnect --drain` с учётом workloads и PDB.
