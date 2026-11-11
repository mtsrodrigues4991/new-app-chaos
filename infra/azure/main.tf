# MVP plus: o new-app-chaos na Azure, ligado só para demonstrar.
#
# Duas partes no mesmo estado:
#   - base (sempre existe, ~US$ 4/mês): IP público fixo com DNS, ACR, identidade do cluster,
#     orçamento. Mantém a URL e as imagens entre uma demo e outra.
#   - cluster (cluster_enabled): AKS com três pools. O workflow "Ligar" aplica com true e o
#     "Desligar" com false, o que apaga só o cluster.
#
# O resource group (new-app) já existe e o GitHub Actions é dono só dele (scripts/azure-setup.sh).

terraform {
  required_version = ">= 1.6"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.14"
    }
  }
  # Preenchido no terraform init pelo workflow (-backend-config): storage account do estado.
  backend "azurerm" {}
}

provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  # O Actions é dono só do RG: não tenta registrar provedores na assinatura.
  resource_provider_registrations = "none"
}

data "azurerm_resource_group" "rg" {
  name = var.resource_group
}

locals {
  suffix = substr(sha1(var.subscription_id), 0, 6)
  tags   = { app = "new-app-chaos", env = "mvp-plus" }
}

# --- base ---

resource "azurerm_public_ip" "ingress" {
  name                = "new-app-chaos-ingress"
  resource_group_name = data.azurerm_resource_group.rg.name
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
  domain_name_label   = "${var.dns_label}-${local.suffix}"
  tags                = local.tags
}

resource "azurerm_container_registry" "acr" {
  name                = "newappchaos${local.suffix}"
  resource_group_name = data.azurerm_resource_group.rg.name
  location            = var.location
  sku                 = "Basic"
  admin_enabled       = false
  tags                = local.tags
}

# Identidade do plano de controle: precisa mexer no IP público, que fica fora do RG dos nós.
resource "azurerm_user_assigned_identity" "cluster" {
  name                = "new-app-chaos-cluster"
  resource_group_name = data.azurerm_resource_group.rg.name
  location            = var.location
  tags                = local.tags
}

resource "azurerm_role_assignment" "cluster_network" {
  scope                = data.azurerm_resource_group.rg.id
  role_definition_name = "Network Contributor"
  principal_id         = azurerm_user_assigned_identity.cluster.principal_id
}

resource "azurerm_consumption_budget_resource_group" "budget" {
  name              = "new-app-chaos"
  resource_group_id = data.azurerm_resource_group.rg.id
  amount            = var.monthly_budget_usd
  time_grain        = "Monthly"
  time_period {
    start_date = "2026-11-01T00:00:00Z"
  }
  notification {
    enabled        = true
    threshold      = 80
    operator       = "GreaterThan"
    threshold_type = "Actual"
    contact_roles  = ["Owner"]
  }
  notification {
    enabled        = true
    threshold      = 100
    operator       = "GreaterThan"
    threshold_type = "Forecasted"
    contact_roles  = ["Owner"]
  }
}

# --- cluster (liga/desliga) ---

resource "azurerm_kubernetes_cluster" "aks" {
  count               = var.cluster_enabled ? 1 : 0
  name                = "new-app-chaos"
  resource_group_name = data.azurerm_resource_group.rg.name
  location            = var.location
  dns_prefix          = "new-app-chaos-${local.suffix}"
  sku_tier            = "Free"
  node_resource_group = "new-app-nodes"
  tags                = local.tags

  # Pool "apps": sistema, Chaos Mesh, new-app-chaos e alvos.
  default_node_pool {
    name                         = "apps"
    vm_size                      = var.apps_vm_size
    node_count                   = var.apps_node_count
    os_disk_size_gb              = 64
    only_critical_addons_enabled = false
    temporary_name_for_rotation  = "appstmp"
  }

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.cluster.id]
  }

  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    load_balancer_sku   = "standard"
  }

  depends_on = [azurerm_role_assignment.cluster_network]
}

# Pool "obs": Grafana (otel-lgtm), longe dos alvos. Taint: só quem tolera entra.
resource "azurerm_kubernetes_cluster_node_pool" "obs" {
  count                 = var.cluster_enabled ? 1 : 0
  name                  = "obs"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.aks[0].id
  vm_size               = var.obs_vm_size
  node_count            = 1
  os_disk_size_gb       = 64
  node_labels           = { pool = "obs" }
  node_taints           = ["pool=obs:NoSchedule"]
  tags = local.tags
}

# Pool "load": a injetora do k6, em máquina spot (bem mais barata; a AKS põe o taint de spot).
resource "azurerm_kubernetes_cluster_node_pool" "load" {
  count                 = var.cluster_enabled ? 1 : 0
  name                  = "load"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.aks[0].id
  vm_size               = var.load_vm_size
  node_count            = 1
  os_disk_size_gb       = 64
  priority              = var.load_spot ? "Spot" : "Regular"
  eviction_policy       = var.load_spot ? "Delete" : null
  spot_max_price        = var.load_spot ? -1 : null
  node_labels           = { pool = "load" }
  # Spot: a própria AKS põe o taint kubernetes.azure.com/scalesetpriority=spot:NoSchedule.
  node_taints = var.load_spot ? [] : ["pool=load:NoSchedule"]
  tags        = local.tags

  lifecycle {
    # Rótulos e taints que a AKS acrescenta sozinha no pool spot não são mudança nossa.
    ignore_changes = [node_labels, node_taints]
  }
}

# Os nós puxam as imagens do ACR.
resource "azurerm_role_assignment" "acr_pull" {
  count                            = var.cluster_enabled ? 1 : 0
  scope                            = azurerm_container_registry.acr.id
  role_definition_name             = "AcrPull"
  principal_id                     = azurerm_kubernetes_cluster.aks[0].kubelet_identity[0].object_id
  skip_service_principal_aad_check = true
}
