# Эксплуатация

CLI принимает `--config` и `--kubeconfig`. В примерах ниже kubeconfig и connection JSON находятся в локальном каталоге `/secure`.

## Смотреть состояние

```sh
kubectl --kubeconfig /secure/cluster.yaml get node k8nnect-vds -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system \
  get configmap connection-status -o yaml
```

ConfigMap содержит `phase`, имя ноды, время и краткое описание. `Connecting` означает выполняющуюся сверку, `Ready` — успешное завершение цикла, `Error` — необходимость диагностики.

## Смотреть логи

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system \
  logs deployment/installer --tail=100
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system \
  logs -l app=k8nnect-gateway --tail=50 --prefix
```

На VDS:

```sh
systemctl status k8nnect-network k8nnect-agent k0sworker
journalctl -u k0sworker -u k8nnect-agent --since '10 minutes ago'
```

Не публикуйте bootstrap-файлы, kubeconfig, приватные ключи или необезличенные снимки конфигурации. `wg show` скрывает private key; `wg show all dump` может раскрыть его.

## Повторить workload-проверки

```sh
k8nnect verify-node \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Проверка создаёт собственные временные Pod, Services и Jobs. Она тестирует новую ноду с каждым существующим Ready Worker и удаляет ресурсы после успеха. При ошибке тестовые ресурсы остаются для диагностики; следующий прогон их очищает.

Повторный `connect` или reconcile аддона поддерживает уже управляемую сеть. Рабочая проверенная нода повторно не присоединяется.

## Отключить Worker

```sh
k8nnect disconnect --drain \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Команда останавливает установщик, cordon/drain новой ноды с учётом PodDisruptionBudget, останавливает принадлежащие ей сервисы и удаляет собственную сеть, Node и кластерные ресурсы соединения.

Пакеты, данные k0s и ключи восстановления на VDS сохраняются. Полный `k0s reset` не выполняется. Живой сценарий удаления пока не сертифицирован: сначала проверьте его на отдельном стенде.

## Обновление и ограничения

После сборки новой версии повторный `addon-install` обновляет установщик. Менять транспортный адрес уже подключённой ноды без явного отключения нельзя. Первичная реализация не предоставляет автоматического обновления Kubernetes и failover основного gateway.
