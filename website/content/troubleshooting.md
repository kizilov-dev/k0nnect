# Диагностика

Начните с `connection-status`, логов установщика и состояния Pod. Статус Ready одной ноды недостаточен для подтверждения всей сети.

## SSH host key mismatch

Убедитесь, что адрес VDS правильный, а Ed25519 fingerprint получен через доверенный канал. Не подменяйте закреплённый ключ только потому, что подключение не прошло. Текущая CLI-проверка явно предпочитает Ed25519.

## Unsupported Calico profile

Проверьте VXLAN Always, IPIP Never, IPv4 и `IP_AUTODETECTION_METHOD=interface=eth1`. Установщик специально отвергает другие настройки: менять CNI всего кластера ради обхода проверки не следует.

## Gateway Pod не Ready

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get events
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system \
  logs -l app=k8nnect-gateway --tail=100 --prefix
```

Проверьте доступ к образу Ubuntu и пакетным репозиториям, поддержку WireGuard, NET_ADMIN и включённый CNI IPv4 forwarding. Наличие процесса не заменяет успешную readiness probe.

## Недоступен controller over gateway

Проверьте UDP между VDS и gateway, свежие handshakes и доступ gateway к приватным портам API и konnectivity. Для ноды без ExternalIP нужен исходящий UDP с ответным трафиком. Полный NAT traversal не предоставляется.

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

## Конфликт владения или адресов

Установщик не перезаписывает чужой Node, namespace, интерфейс или конфигурацию существующего k0s. Проверьте имена и наличие старой установки. Для миграции управляемого адреса сначала используйте `disconnect --drain` с учётом workloads и PDB.
