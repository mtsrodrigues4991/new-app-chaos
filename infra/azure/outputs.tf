output "fqdn" {
  value = azurerm_public_ip.ingress.fqdn
}

output "public_ip_name" {
  value = azurerm_public_ip.ingress.name
}

output "public_ip" {
  value = azurerm_public_ip.ingress.ip_address
}

output "acr_login_server" {
  value = azurerm_container_registry.acr.login_server
}

output "acr_name" {
  value = azurerm_container_registry.acr.name
}

output "cluster_name" {
  value = var.cluster_enabled ? azurerm_kubernetes_cluster.aks[0].name : ""
}

output "resource_group" {
  value = data.azurerm_resource_group.rg.name
}
