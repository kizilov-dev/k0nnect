# Совместимость и границы

k8nnect расширяет **один существующий кластер**. Объединение независимых control plane, межкластерный DNS и экспорт Service пока не реализованы.

## Поддержанный профиль

| Компонент | Требование |
| --- | --- |
| Kubernetes | k0s; исходные Ready Workers одной minor-версии |
| Архитектура | Linux/amd64 |
| CNI | Calico, Flannel, Cilium или kube-router — см. таблицу ниже |
| VDS | root SSH, apt, systemd, Python 3, kernel WireGuard |
| API | TLS-проверяемый kubeconfig, client certificate или token |
| Установка | Права на RBAC, bootstrap и ресурсы аддона |

## CNI

Установщик сам определяет CNI по его DaemonSet и отвергает настройки, при которых Pod-трафик не пройдёт через маршрутизируемый WireGuard-транспорт. Общее условие для всех: адрес ноды выбирается по интерфейсу `eth1` (или по интерфейсу маршрута по умолчанию), а Pod-трафик между нодами инкапсулируется. Внутри namespace Worker на VDS `eth1` — это WireGuard-интерфейс, поэтому существующая настройка CNI выбирает транспортный адрес без изменений.

| CNI | Обязательные настройки | Отвергается |
| --- | --- | --- |
| Calico | IPv4 IPPool, VXLAN Always, IPIP Never, `IP_AUTODETECTION_METHOD=interface=eth1` | IPIP, BGP без инкапсуляции, другая autodetection |
| Flannel | Backend `vxlan`, `--kube-subnet-mgr`, `--iface=eth1` или без `--iface` | host-gw, `DirectRouting`, `--iface-regex`, `--iface-can-reach`, `--public-ip` |
| Cilium | `routing-mode=tunnel` (VXLAN или Geneve), IPAM `cluster-pool` или `kubernetes`, `devices` пустой или `eth1` | native routing, другие `devices`, прозрачное шифрование WireGuard/IPsec |
| kube-router | `--run-router`, overlay включён (`subnet` или `full`), full-mesh BGP | `--enable-overlay=false`, внешние BGP-пиры, route reflectors |

Во всех случаях Pod-сеть и Service IP не должны пересекаться с transport /24.

Реальные испытания проведены на управляемых кластерах k0s 1.36.4 публичного облачного провайдера: Calico 3.32.2, Flannel 0.28.9, Cilium 1.20.1 и kube-router 2.10.0. Результаты — в [проверках](verification.md). Успех на этих связках не означает совместимость со всеми версиями.

### Особенности CNI

- **Flannel и Cilium** не устанавливают стандартные CNI-плагины (`loopback`, `bridge`, `host-local`, `portmap`): на нодах управляемого кластера их ставит провайдер. Если их нет на VDS, установщик добавляет плагины containernetworking/plugins закреплённой версии, сверяя sha256. Уже существующие файлы не заменяются.
- **Cilium** с kube-proxy replacement и `node-port-bind-protection` запрещает `bind()` на порты NodePort-сервисов на каждой ноде. Поэтому gateway в таком кластере слушает UDP-порт напрямую, без NodePort Service. Порт не резервируется в Kubernetes: не назначайте его вручную другим NodePort-сервисам.
- **kube-router** добавляет подсеть новой ноды в ipset исключений masquerade только при периодической синхронизации (`--routes-sync-period`, по умолчанию 5 минут). До этого ответы Pod с VDS теряются. Проверка ждёт это окно и повторяет тесты, прежде чем признать ошибку.
- **kube-router** строит IPIP-туннель к VDS поверх WireGuard: MTU туннеля 1380, а у Pod на исходных нодах обычно 1500. Первая UDP-датаграмма больше пути с DF теряется, пока ядро отправителя не узнает MTU из ICMP; следующие фрагментируются. TCP это не затрагивает.
- **Flannel и Cilium** вычисляют MTU Pod от интерфейса ноды. Поэтому `eth1` на VDS получает MTU сети исходных нод, а не транспортный MTU (см. [MTU](configuration.md)). Calico на проверенном стенде задаёт MTU VXLAN явно, но получает тот же `eth1`.

## Сетевые условия

- Transport /24 не пересекается с адресами нод, Pod-пулами и Service IP.
- На Workers разрешён UDP-порт gateway (`port`); на VDS — UDP `port + 1`.
- Из сети Worker доступны API и приватный порт konnectivity контроллера.
- Workers и VDS имеют доступ к необходимым пакетам, release-файлам и образам.
- IPv4 forwarding уже включён CNI на gateway-нодах.

Worker без ExternalIP инициирует WireGuard к VDS. Это работает при разрешённых исходящем UDP и ответном трафике. Произвольный NAT traversal и relay не реализованы.

## Текущие ограничения

- Одна внешняя VDS на кластер и один управляемый Worker на VDS.
- IPv4, статически выбранный основной gateway, без автоматического failover.
- Установщик конкретного профиля использует WireGuard; другие транспорты требуют реализации.
- Перенос адреса существующей ноды требует явного отключения.
- Полная перезагрузка хоста, длительная нагрузка, NetworkPolicy, CSI и облачные LoadBalancer не сертифицированы.
- Нет CRD, Helm chart, UI управления и готового release-образа установщика.

## Почему CNI имеет значение

CNI может управлять BGP, eBPF, адресами нод, инкапсуляцией и маршрутами. Универсальная запись статических маршрутов здесь недостаточна: k8nnect не меняет настройки CNI, а принимает только те, что работают поверх его транспорта. Для каждого нового профиля нужны проверки IPAM, endpoint selection, policy, MTU и реальных Pod.

Статический kubeadm/Flannel-прототип остаётся отдельным режимом. Он настраивает транспорт, но не выполняет автоматическое подключение Worker.
