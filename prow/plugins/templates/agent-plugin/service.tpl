apiVersion: v1
kind: Service
metadata:
  name: agent-plugin
  namespace: prow
  labels:
    app: agent-plugin
spec:
  selector:
    app: agent-plugin
  ports:
  - name: http
    port: 8080
    protocol: TCP
    targetPort: 8080
  type: ClusterIP
